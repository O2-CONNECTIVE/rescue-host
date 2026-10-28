package backend

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 子制御は全て試験専用。同一userから保護されたIPC/supervisorではない。
// JSONにはIdentity、公開鍵、親が保持するfixture宛先だけを許す。
type sessionChildInit struct {
	Identity              Identity
	ClientPublic          []byte
	Target, Gateway, Mode string
}
type sessionChildCommand struct {
	Identity  Identity
	Operation string
	Sequence  uint64
}
type sessionChildReply struct {
	Address     string
	HostPublic  []byte
	PID         int
	Failed      bool
	Snapshot    Snapshot
	JobAttached bool
	Failure     string
}

var childEvidence []map[string]any

func fixtureAddress(address string) bool {
	h, p, e := net.SplitHostPort(address)
	n, ne := strconv.Atoi(p)
	return e == nil && ne == nil && h == "127.0.0.1" && n > 1024 && n <= 65535 && n != 22
}

func sessionFixtureChild() int {
	// pipe、鍵生成、dial、Closeより先に独立した実時間上限を開始する。
	ttl := time.AfterFunc(3*time.Second, func() { os.Exit(124) })
	defer ttl.Stop()
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4096))
	decoder.DisallowUnknownFields()
	var init sessionChildInit
	if decoder.Decode(&init) != nil || !fixtureAddress(init.Target) || !fixtureAddress(init.Gateway) || (init.Mode != "normal" && init.Mode != "fault") {
		return 2
	}
	// 初期DTO受信後に割り当てる。拒否時もpipe書込との競合で具体理由を失わない。
	jobAttached, jobErr := inheritFixtureJob()
	if jobErr != nil {
		json.NewEncoder(os.Stdout).Encode(sessionChildReply{PID: os.Getpid(), Failed: true, Failure: jobErr.Error()})
		return 125
	}
	pub, _, options, rest, err := ssh.ParseAuthorizedKey(init.ClientPublic)
	if err != nil || len(options) != 0 || len(rest) != 0 {
		return 3
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return 4
	}
	host, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return 5
	}
	cfg := Config{Identity: init.Identity, OperationMode: "restricted", AllowedKeys: []ssh.PublicKey{pub}, HostKey: host, MaximumDuration: 10 * time.Second, LeaseDuration: 10 * time.Second, MaxChannels: 1, MaxHandshakes: 1, MaxTransports: 2}
	var fail atomic.Bool
	fail.Store(init.Mode == "fault")
	// 親が保持している高位fixtureをこの子の寿命内だけ借りる。製品設定には存在しない。
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", init.Target)
		if e != nil {
			return nil, nil, e
		}
		raw := conn.(*net.TCPConn)
		stream := &tcpStream{raw}
		if init.Mode == "fault" {
			return &heldStop{stream, &fail}, heldCutoff(raw, &fail), nil
		}
		return stream, tcpCutoff(raw), nil
	})
	if err != nil {
		return 6
	}
	if s.Ready(init.Identity) != nil || s.Activate(init.Identity) != nil {
		return 7
	}
	// 待受は親が所有し続ける。子の終了とport再利用の隙間に親が別serviceへdialしない。
	conn, err := net.DialTimeout("tcp4", init.Gateway, time.Second)
	if err != nil {
		return 8
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		raw := conn.(*net.TCPConn)
		var e error
		if init.Mode == "fault" {
			e = s.serve(&heldStop{&tcpStream{raw}, &fail}, heldCutoff(raw, &fail))
		} else {
			e = s.Serve(raw)
		}
		if errors.Is(e, ErrNotAccepted) {
			raw.Close()
		}
	}()
	stop := func() bool {
		fail.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		e := s.Close(ctx)
		cancel()
		if e != nil {
			return false
		}
		wg.Wait()
		return true
	}
	defer stop()
	encoder := json.NewEncoder(os.Stdout)
	if encoder.Encode(sessionChildReply{Address: init.Gateway, HostPublic: ssh.MarshalAuthorizedKey(host.PublicKey()), PID: os.Getpid(), Snapshot: s.Snapshot(), JobAttached: jobAttached}) != nil {
		return 10
	}
	for {
		var command sessionChildCommand
		if err = decoder.Decode(&command); err != nil {
			if errors.Is(err, io.EOF) && stop() {
				return 0
			}
			return 11
		}
		var operationErr error
		switch command.Operation {
		case "renew":
			operationErr = s.Renew(command.Identity, command.Sequence)
		case "revoke":
			operationErr = s.Revoke(command.Identity)
			if operationErr == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				operationErr = s.Close(ctx)
				cancel()
			}
		default:
			return 12
		}
		if encoder.Encode(sessionChildReply{PID: os.Getpid(), Failed: operationErr != nil, Snapshot: s.Snapshot()}) != nil {
			return 13
		}
		if command.Operation == "revoke" && operationErr == nil {
			return 0
		}
	}
}

type ownedSessionChild struct {
	cmd                          *exec.Cmd
	input                        io.WriteCloser
	decoder                      *json.Decoder
	done                         chan error
	exited                       bool
	waitErr                      error
	stderr                       bytes.Buffer
	ready                        sessionChildReply
	budget                       *fixtureBudget
	gateway                      *trackedListener
	startedUTC                   string
	startedMono                  time.Time
	killRequested, killSucceeded bool
	killError                    string
}

// 開始判定から所有登録まで同じlockを使い、開始途中の子も取りこぼさない。
// 注入時計は試験予算だけに使用し、OS時計を変更しない。
type fixtureBudget struct {
	sync.Mutex
	now      func() time.Time
	deadline time.Time
	children map[*ownedSessionChild]bool
	prepare  func(*exec.Cmd) error
}

func startSessionChild(t *testing.T, init sessionChildInit, budget *fixtureBudget) (*ownedSessionChild, error) {
	t.Helper()
	if budget != nil {
		budget.Lock()
		expired := !budget.now().Before(budget.deadline)
		budget.Unlock()
		if expired {
			return nil, errors.New("試験予算期限切れ: 子未開始")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	r := &ownedSessionChild{cmd: exec.Command(exe, "-test.run=^$"), done: make(chan error, 1), budget: budget}
	r.gateway = testListen(t)
	init.Gateway = r.gateway.Addr().String()
	r.cmd.Env = append(os.Environ(), "RESCUE_SESSION_FIXTURE_CHILD=1", "RESCUE_SESSION_JOB_HANDLE=")
	r.input, err = r.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := r.cmd.StdoutPipe()
	if err != nil {
		r.input.Close()
		return nil, err
	}
	r.decoder = json.NewDecoder(io.LimitReader(output, 8192))
	r.cmd.Stderr = &r.stderr
	if budget != nil {
		budget.Lock()
		if !budget.now().Before(budget.deadline) {
			budget.Unlock()
			r.input.Close()
			output.Close()
			return nil, errors.New("試験予算期限切れ: 子未開始")
		}
		if budget.prepare != nil {
			if err = budget.prepare(r.cmd); err != nil {
				budget.Unlock()
				r.input.Close()
				output.Close()
				return nil, err
			}
		}
	}
	r.startedMono = time.Now()
	r.startedUTC = r.startedMono.UTC().Format(time.RFC3339Nano)
	err = r.cmd.Start()
	if err == nil && budget != nil {
		budget.children[r] = true
	}
	if budget != nil {
		budget.Unlock()
	}
	if err != nil {
		r.input.Close()
		output.Close()
		return nil, err
	}
	go func() { r.done <- r.cmd.Wait() }()
	t.Cleanup(func() {
		if !r.exited {
			r.wait(t, true, time.Second)
		}
		r.input.Close()
	})
	if budget != nil {
		budget.Lock()
		expired := !budget.now().Before(budget.deadline)
		budget.Unlock()
		if expired {
			r.wait(t, true, time.Second)
			return nil, errors.New("開始中に試験予算期限到達")
		}
	}
	if err = json.NewEncoder(r.input).Encode(init); err != nil {
		return nil, err
	}
	r.ready = r.reply(t)
	if r.ready.Failed {
		return nil, errors.New("自己子準備拒否: " + r.ready.Failure)
	}
	if r.ready.PID != r.cmd.Process.Pid || r.ready.Address != r.gateway.Addr().String() || r.ready.Snapshot.State != "active" {
		return nil, errors.New("自己子準備応答が不正")
	}
	return r, nil
}

func (r *ownedSessionChild) reply(t *testing.T) sessionChildReply {
	t.Helper()
	type decoded struct {
		r sessionChildReply
		e error
	}
	done := make(chan decoded, 1)
	go func() { var v sessionChildReply; e := r.decoder.Decode(&v); done <- decoded{v, e} }()
	select {
	case v := <-done:
		if v.e != nil {
			t.Fatalf("子の応答なし: %v", v.e)
		}
		return v.r
	case <-time.After(time.Second):
		t.Fatal("自己子応答が1秒超過")
	}
	return sessionChildReply{}
}

func (r *ownedSessionChild) command(t *testing.T, c sessionChildCommand) sessionChildReply {
	t.Helper()
	if e := json.NewEncoder(r.input).Encode(c); e != nil {
		t.Fatal(e)
	}
	return r.reply(t)
}

func (r *ownedSessionChild) wait(t *testing.T, kill bool, limit time.Duration) error {
	t.Helper()
	if r.exited {
		return r.waitErr
	}
	if kill {
		r.killRequested = true
		e := r.cmd.Process.Kill()
		r.killSucceeded = e == nil
		if e != nil {
			r.killError = e.Error()
		}
		if e != nil && !errors.Is(e, os.ErrProcessDone) {
			t.Errorf("所有子Kill: %v", e)
		}
	}
	select {
	case r.waitErr = <-r.done:
		r.exited = true
	case <-time.After(limit):
		t.Fatal("所有子Wait未確認")
	}
	if r.budget != nil {
		r.budget.Lock()
		delete(r.budget.children, r)
		r.budget.Unlock()
	}
	if r.stderr.Len() != 0 {
		t.Errorf("子stderrあり: %s", r.stderr.String())
	}
	childEvidence = append(childEvidence, map[string]any{"pid": r.cmd.Process.Pid, "started_utc": r.startedUTC, "wait_confirmed": r.exited, "wait_exit_code": r.cmd.ProcessState.ExitCode(), "kill_requested": r.killRequested, "kill_call_succeeded": r.killSucceeded, "kill_error": r.killError, "protected_supervisor": false})
	return r.waitErr
}

func (r *ownedSessionChild) client(t *testing.T, cfg Config, signer ssh.Signer) *ssh.Client {
	t.Helper()
	if r.exited || !fixtureAddress(r.ready.Address) {
		t.Fatal("自己子の終了を確認済み")
	}
	select {
	case e := <-r.done:
		r.done <- e
		r.wait(t, false, time.Second)
		t.Fatal("接続前に自己子の終了を検出")
	default:
	}
	host, _, options, rest, e := ssh.ParseAuthorizedKey(r.ready.HostPublic)
	if e != nil || len(options) != 0 || len(rest) != 0 {
		t.Fatal("子host公開鍵が不正")
	}
	// 親はdialしない。保持し続ける自己listenerで子からの接続を受け、pipeのhost公開鍵でpinする。
	r.gateway.Listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	c, e := r.gateway.Accept()
	if e != nil {
		t.Fatal(e)
	}
	conn := track(c)
	conn.SetDeadline(time.Now().Add(time.Second))
	sc, ch, req, e := ssh.NewClientConn(conn, r.ready.Address, &ssh.ClientConfig{User: cfg.Identity.LaunchID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host)})
	if e != nil {
		conn.Close()
		t.Fatal(e)
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, ch, req)
}

func childFixture(t *testing.T, mode string, budget *fixtureBudget) (*ownedSessionChild, *echoFixture, Config, *ssh.Client) {
	t.Helper()
	f := fixture(t)
	cfg, signer := config(t, "restricted")
	r, e := startSessionChild(t, sessionChildInit{Identity: cfg.Identity, ClientPublic: ssh.MarshalAuthorizedKey(signer.PublicKey()), Target: f.l.Addr().String(), Mode: mode}, budget)
	if e != nil {
		t.Fatal(e)
	}
	cl := r.client(t, cfg, signer)
	t.Cleanup(func() { cl.Close() })
	ch, e := open(cl, "127.0.0.1", 22)
	if e != nil {
		t.Fatal(e)
	}
	echo(t, ch)
	return r, f, cfg, cl
}

func assertChildTargetEOF(t *testing.T, f *echoFixture, deadline time.Time) {
	t.Helper()
	select {
	case event := <-f.events:
		if e, ok := event.err.(net.Error); ok && e.Timeout() {
			t.Fatal("timeoutをtargetEOFと誤認")
		}
	case <-time.After(time.Until(deadline)):
		t.Fatal("子所有targetEOFが期限超過")
	}
}

func TestCloseoutN7OwnedSessionLifetime(t *testing.T) {
	for _, mode := range []string{"normal-revoke", "pipe-eof", "kill-after-fault"} {
		t.Run(mode, func(t *testing.T) {
			childMode := "normal"
			if mode == "kill-after-fault" {
				childMode = "fault"
			}
			r, f, cfg, cl := childFixture(t, childMode, nil)
			wrong := cfg.Identity
			wrong.Epoch = "wrong-child-epoch"
			v := r.command(t, sessionChildCommand{wrong, "renew", 999})
			if !v.Failed || v.Snapshot.State != "active" {
				t.Fatal("別epoch更新の拒否が不正")
			}
			v = r.command(t, sessionChildCommand{cfg.Identity, "renew", 1})
			if v.Failed {
				t.Fatal("正しい更新を拒否")
			}
			trigger := time.Now()
			switch mode {
			case "normal-revoke":
				v = r.command(t, sessionChildCommand{cfg.Identity, "revoke", 0})
				if v.Failed || v.Snapshot.State != "terminated" {
					t.Fatal("正常子終了未確認")
				}
				if e := r.wait(t, false, time.Second); e != nil {
					t.Fatal(e)
				}
			case "pipe-eof":
				r.input.Close()
				if e := r.wait(t, false, time.Second); e != nil {
					t.Fatal(e)
				}
			case "kill-after-fault":
				v = r.command(t, sessionChildCommand{cfg.Identity, "revoke", 0})
				if !v.Failed || v.Snapshot.State != "revoking" || v.Snapshot.Owned != 2 {
					t.Fatalf("故障中の参照喪失: %+v", v)
				}
				if e := r.wait(t, true, time.Second); e == nil {
					t.Fatal("自己子強制停止証拠なし")
				}
			}
			assertChildTargetEOF(t, f, trigger.Add(time.Second))
			done := make(chan error, 1)
			go func() { done <- cl.Wait() }()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("SSH終了なし")
				}
			case <-time.After(time.Until(trigger.Add(time.Second))):
				t.Fatal("子SSH終了が1秒超過")
			}
			t.Logf("mode=%s child_pid=%d wait=true target_and_ssh_end_ms=%d protected_supervisor=false", mode, r.cmd.Process.Pid, time.Since(trigger).Milliseconds())
		})
	}
}

func TestCloseoutN8DeadlineAndTTL(t *testing.T) {
	t.Run("injected-budget", func(t *testing.T) {
		now := time.Now()
		budget := &fixtureBudget{now: func() time.Time { return now }, deadline: now.Add(time.Minute), children: map[*ownedSessionChild]bool{}}
		r, f, _, _ := childFixture(t, "normal", budget)
		budget.Lock()
		now = budget.deadline
		budget.Unlock()
		if _, e := startSessionChild(t, sessionChildInit{}, budget); e == nil {
			t.Fatal("期限切れ後の新しい子を開始")
		}
		budget.Lock()
		count := len(budget.children)
		var children []*ownedSessionChild
		for c := range budget.children {
			children = append(children, c)
		}
		budget.Unlock()
		if count != 1 {
			t.Fatal("自己子所有記録が不正")
		}
		trigger := time.Now()
		for _, c := range children {
			c.wait(t, true, time.Second)
		}
		assertChildTargetEOF(t, f, trigger.Add(time.Second))
		if !r.exited {
			t.Fatal("進行中自己子の回収なし")
		}
		budget.Lock()
		remaining := len(budget.children)
		budget.Unlock()
		if remaining != 0 {
			t.Fatal("子所有記録残留")
		}
		t.Log("注入予算期限: 新規開始拒否・進行中自己子をhandle限定Kill/Wait、OS時計変更なし")
	})
	t.Run("independent-three-second-ttl", func(t *testing.T) {
		r, f, _, _ := childFixture(t, "normal", nil)
		trigger := time.Now()
		e := r.wait(t, false, 4*time.Second)
		var exit *exec.ExitError
		if !errors.As(e, &exit) || exit.ExitCode() != 124 {
			t.Fatalf("独立TTL終了が不正: %v", e)
		}
		assertChildTargetEOF(t, f, trigger.Add(4*time.Second))
		t.Logf("child_pid=%d independent_ttl_exit=124 elapsed_ms=%d", r.cmd.Process.Pid, time.Since(trigger).Milliseconds())
	})
}
