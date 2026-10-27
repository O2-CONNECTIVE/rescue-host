package backend

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// This client implements only the bounded fixture conversation below. In
// particular, it never acknowledges CHANNEL_CLOSE automatically. A normal
// ssh.Client does, so omitting Channel.Close at application level cannot test
// a peer which withholds the SSH close handshake. No production code uses it.
// RFC 4253 sections 6-8, RFC 4252 section 7, and RFC 8731 define the wire forms.
type lifecycleWireClient struct {
	conn                        net.Conn
	reader                      *bufio.Reader
	readCipher, writeCipher     cipher.Stream
	readMAC, writeMAC           []byte
	readSequence, writeSequence uint32
}

func lifecycleString(dst, value []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func lifecycleStrings(kind byte, values ...string) []byte {
	p := []byte{kind}
	for _, v := range values {
		p = lifecycleString(p, []byte(v))
	}
	return p
}

func lifecycleMAC(key []byte, seq uint32, packet []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(binary.BigEndian.AppendUint32(nil, seq))
	h.Write(packet)
	return h.Sum(nil)
}

func (c *lifecycleWireClient) write(payload []byte) error {
	if len(payload) == 0 || len(payload) > 32768 {
		return errors.New("fixture payload limit")
	}
	block := 8
	if c.writeCipher != nil {
		block = aes.BlockSize
	}
	padding := block - (len(payload)+5)%block
	if padding < 4 {
		padding += block
	}
	packet := binary.BigEndian.AppendUint32(nil, uint32(1+len(payload)+padding))
	packet = append(packet, byte(padding))
	packet = append(packet, payload...)
	pad := make([]byte, padding)
	if _, err := io.ReadFull(rand.Reader, pad); err != nil {
		return err
	}
	packet = append(packet, pad...)
	var mac []byte
	if c.writeCipher != nil {
		mac = lifecycleMAC(c.writeMAC, c.writeSequence, packet)
		c.writeCipher.XORKeyStream(packet, packet)
	}
	c.writeSequence++
	_, err := io.Copy(c.conn, bytes.NewReader(append(packet, mac...)))
	return err
}

func (c *lifecycleWireClient) read() ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return nil, err
	}
	if c.readCipher != nil {
		c.readCipher.XORKeyStream(header, header)
	}
	length := binary.BigEndian.Uint32(header)
	if length < 6 || length > 65536 {
		return nil, fmt.Errorf("fixture packet length %d", length)
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, err
	}
	if c.readCipher != nil {
		c.readCipher.XORKeyStream(body, body)
		mac := make([]byte, sha256.Size)
		if _, err := io.ReadFull(c.reader, mac); err != nil {
			return nil, err
		}
		if !hmac.Equal(mac, lifecycleMAC(c.readMAC, c.readSequence, append(header, body...))) {
			return nil, errors.New("fixture SSH MAC mismatch")
		}
	}
	c.readSequence++
	padding := int(body[0])
	if padding < 4 || padding+1 >= len(body) {
		return nil, errors.New("fixture invalid SSH padding")
	}
	return body[1 : len(body)-padding], nil
}

func (c *lifecycleWireClient) expect(kind byte) ([]byte, error) {
	p, err := c.read()
	if err != nil {
		return nil, err
	}
	if p[0] != kind {
		return nil, fmt.Errorf("fixture expected SSH message %d, got %d", kind, p[0])
	}
	return p[1:], nil
}

func lifecycleHandshake(conn net.Conn, cfg Config, signer ssh.Signer) (*lifecycleWireClient, error) {
	c := &lifecycleWireClient{conn: conn, reader: bufio.NewReaderSize(conn, 512)}
	clientVersion := []byte("SSH-2.0-rescue-lifecycle-fixture")
	if _, err := conn.Write(append(append([]byte(nil), clientVersion...), '\r', '\n')); err != nil {
		return nil, err
	}
	// ReadSlice has a fixed buffer; a peer cannot supply an unbounded banner.
	versionLine, err := c.reader.ReadSlice('\n')
	if err != nil || len(versionLine) > 255 {
		return nil, fmt.Errorf("fixture version: %v", err)
	}
	serverVersion := []byte(strings.TrimRight(string(versionLine), "\r\n"))
	if !bytes.HasPrefix(serverVersion, []byte("SSH-2.0-")) {
		return nil, errors.New("fixture SSH version mismatch")
	}
	clientKex := make([]byte, 17)
	clientKex[0] = 20
	if _, err = io.ReadFull(rand.Reader, clientKex[1:]); err != nil {
		return nil, err
	}
	// Do not advertise strict KEX or ext-info: sequence numbers continue across
	// NEWKEYS, and the exact conversation has no extension/rekey handling.
	for _, list := range []string{"curve25519-sha256", "ssh-ed25519", "aes128-ctr", "aes128-ctr", "hmac-sha2-256", "hmac-sha2-256", "none", "none", "", ""} {
		clientKex = lifecycleString(clientKex, []byte(list))
	}
	clientKex = append(clientKex, 0, 0, 0, 0, 0)
	if err = c.write(clientKex); err != nil {
		return nil, err
	}
	serverKex, err := c.expect(20)
	if err != nil {
		return nil, err
	}
	serverKex = append([]byte{20}, serverKex...)
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	clientPublic := private.PublicKey().Bytes()
	if err = c.write(lifecycleString([]byte{30}, clientPublic)); err != nil {
		return nil, err
	}
	replyBytes, err := c.expect(31)
	if err != nil {
		return nil, err
	}
	var reply struct{ Host, Public, Signature []byte }
	if err = ssh.Unmarshal(replyBytes, &reply); err != nil {
		return nil, err
	}
	if !bytes.Equal(reply.Host, cfg.HostKey.PublicKey().Marshal()) {
		return nil, errors.New("fixture host key pin mismatch")
	}
	peer, err := ecdh.X25519().NewPublicKey(reply.Public)
	if err != nil {
		return nil, err
	}
	secret, err := private.ECDH(peer)
	if err != nil {
		return nil, err
	}
	// SSH interprets the X25519 output as an unsigned big-endian mpint.
	for len(secret) > 0 && secret[0] == 0 {
		secret = secret[1:]
	}
	if len(secret) > 0 && secret[0]&128 != 0 {
		secret = append([]byte{0}, secret...)
	}
	encodedSecret := lifecycleString(nil, secret)
	var exchange []byte
	for _, value := range [][]byte{clientVersion, serverVersion, clientKex, serverKex, reply.Host, clientPublic, reply.Public} {
		exchange = lifecycleString(exchange, value)
	}
	exchange = append(exchange, encodedSecret...)
	hash := sha256.Sum256(exchange)
	var signature ssh.Signature
	if err = ssh.Unmarshal(reply.Signature, &signature); err != nil {
		return nil, err
	}
	if err = cfg.HostKey.PublicKey().Verify(hash[:], &signature); err != nil {
		return nil, err
	}
	derive := func(tag byte) []byte {
		h := sha256.New()
		h.Write(encodedSecret)
		h.Write(hash[:])
		h.Write([]byte{tag})
		h.Write(hash[:])
		return h.Sum(nil)
	}
	if err = c.write([]byte{21}); err != nil {
		return nil, err
	}
	if _, err = c.expect(21); err != nil {
		return nil, err
	}
	writeBlock, err := aes.NewCipher(derive('C')[:16])
	if err != nil {
		return nil, err
	}
	readBlock, err := aes.NewCipher(derive('D')[:16])
	if err != nil {
		return nil, err
	}
	c.writeCipher = cipher.NewCTR(writeBlock, derive('A')[:16])
	c.readCipher = cipher.NewCTR(readBlock, derive('B')[:16])
	c.writeMAC, c.readMAC = derive('E'), derive('F')
	if err = c.write(lifecycleStrings(5, "ssh-userauth")); err != nil {
		return nil, err
	}
	service, err := c.expect(6)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(service, lifecycleString(nil, []byte("ssh-userauth"))) {
		return nil, errors.New("fixture userauth service mismatch")
	}
	auth := lifecycleStrings(50, cfg.Identity.LaunchID, "ssh-connection", "publickey")
	auth = append(auth, 1)
	auth = lifecycleString(auth, []byte("ssh-ed25519"))
	auth = lifecycleString(auth, signer.PublicKey().Marshal())
	signed := append(lifecycleString(nil, hash[:]), auth...)
	sig, err := signer.Sign(rand.Reader, signed)
	if err != nil {
		return nil, err
	}
	sigBytes := lifecycleString(nil, []byte(sig.Format))
	sigBytes = lifecycleString(sigBytes, sig.Blob)
	if err = c.write(lifecycleString(auth, sigBytes)); err != nil {
		return nil, err
	}
	if _, err = c.expect(52); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *lifecycleWireClient) open(id uint32) ([]byte, error) {
	p := lifecycleStrings(90, "direct-tcpip")
	p = binary.BigEndian.AppendUint32(p, id)
	p = binary.BigEndian.AppendUint32(p, 65536)
	p = binary.BigEndian.AppendUint32(p, 32768)
	p = append(p, ssh.Marshal(wireTarget{"127.0.0.1", 22, "127.0.0.1", 1234})...)
	if err := c.write(p); err != nil {
		return nil, err
	}
	return c.read()
}

func (c *lifecycleWireClient) eofAndWithholdClose(confirmation []byte, id uint32) error {
	if len(confirmation) != 17 || confirmation[0] != 91 || binary.BigEndian.Uint32(confirmation[1:5]) != id {
		return errors.New("fixture invalid channel confirmation")
	}
	serverID := binary.BigEndian.Uint32(confirmation[5:9])
	if err := c.write(binary.BigEndian.AppendUint32([]byte{96}, serverID)); err != nil {
		return err
	}
	seenEOF := false
	for n := 0; n < 8; n++ {
		p, err := c.read()
		if err != nil {
			return err
		}
		if len(p) < 5 || binary.BigEndian.Uint32(p[1:5]) != id {
			return errors.New("fixture unexpected channel packet")
		}
		switch p[0] {
		case 96:
			seenEOF = true
		case 97:
			if !seenEOF {
				return errors.New("fixture CLOSE without preceding EOF")
			}
			return nil // Deliberately do not transmit the reciprocal CLOSE.
		case 93: // A bounded window adjustment is harmless.
		default:
			return fmt.Errorf("fixture unexpected channel message %d", p[0])
		}
	}
	return errors.New("fixture channel message budget exhausted")
}

type lifecycleTarget struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *lifecycleTarget) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}
func (c *lifecycleTarget) Close() error {
	err := c.Conn.Close()
	if err == nil {
		c.once.Do(func() { close(c.closed) })
	}
	return err
}

func TestChannelCloseHandshakeKeepsCapacity(t *testing.T) {
	f := fixture(t)
	cfg, signer := config(t, "restricted")
	cfg.MaxChannels, cfg.MaxTransports = 1, 1
	cfg.MaximumDuration, cfg.LeaseDuration = 10*time.Second, 10*time.Second
	closed := make(chan chan struct{}, 5)
	var dials atomic.Int32
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, raw, e := fixtureTarget(ctx, f.l.Addr().String())
		if e != nil {
			return nil, nil, e
		}
		dials.Add(1)
		done := make(chan struct{})
		closed <- done
		return &lifecycleTarget{Conn: conn, closed: done}, tcpCutoff(raw), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Restrict algorithms only on this test instance, before it starts serving.
	s.config.KeyExchanges = []string{"curve25519-sha256"}
	s.config.Ciphers = []string{"aes128-ctr"}
	s.config.MACs = []string{"hmac-sha2-256"}
	address := start(t, s, cfg)
	conn, err := testDial(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wire, err := lifecycleHandshake(conn, cfg, signer)
	if err != nil {
		t.Fatalf("bounded wire handshake: %v", err)
	}
	baseline := s.workers.Count()
	first, err := wire.open(0)
	if err != nil {
		t.Fatalf("first fixture channel: %v", err)
	}
	if err = wire.eofAndWithholdClose(first, 0); err != nil {
		t.Fatal(err)
	}
	waitTarget := func() {
		t.Helper()
		select {
		case done := <-closed:
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("target adapter close not completed")
			}
		case <-time.After(time.Second):
			t.Fatal("fixture target was not dialed")
		}
		awaitSnapshot(t, s, func(v Snapshot) bool { return v.Closing == 0 })
	}
	waitTarget()
	accepted := 0
	for id := uint32(1); id <= 4; id++ {
		p, e := wire.open(id)
		if e != nil {
			// A deliberate transport shutdown is also a safe admission outcome;
			// a timeout is not evidence of either rejection or shutdown.
			if ne, ok := e.(net.Error); ok && ne.Timeout() {
				t.Fatalf("open stalled instead of rejecting: %v", e)
			}
			var networkError *net.OpError
			if !errors.Is(e, io.EOF) && !errors.Is(e, io.ErrUnexpectedEOF) && !errors.Is(e, net.ErrClosed) && !errors.As(e, &networkError) {
				t.Fatalf("wire transport error: %v", e)
			}
			break
		}
		if p[0] == 1 {
			break
		} // SSH_MSG_DISCONNECT is an explicit shutdown.
		if p[0] == 92 {
			if len(p) < 9 || binary.BigEndian.Uint32(p[1:5]) != id {
				t.Fatal("invalid channel rejection")
			}
			continue
		}
		if p[0] != 91 {
			t.Fatalf("unexpected open response %d", p[0])
		}
		accepted++
		if err = wire.eofAndWithholdClose(p, id); err != nil {
			t.Fatal(err)
		}
		waitTarget()
	}
	t.Logf("withheld_CLOSE=true extra_accepted=%d target_dials=%d workers_baseline=%d workers_after=%d snapshot=%+v", accepted, dials.Load(), baseline, s.workers.Count(), s.Snapshot())
	if accepted != 0 || dials.Load() != 1 {
		t.Fatalf("MaxChannels=1 reused before peer CLOSE: accepted=%d target_dials=%d", accepted, dials.Load())
	}
	// Closing this owned fixture transport must release the unacknowledged
	// channel and all workers; no leaked peer or listener survives the test.
	conn.Close()
	closeSession(t, s)
	if s.workers.Count() != 0 || s.Snapshot().Owned != 0 {
		t.Fatal("wire fixture cleanup left owners/workers")
	}
}
