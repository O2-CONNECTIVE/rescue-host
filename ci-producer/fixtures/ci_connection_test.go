//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exactly this explicitly named test is live. Compile-only is safe; executing
// requires a disposable CI runner and independent source review. Never run it
// on an interactive desktop to diagnose a failed or unknown cleanup.
func TestCIActualProducerConnection(t *testing.T) {
	if os.Getenv("CI_ACTUAL_PRODUCER") != "1" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("RUNNER_OS") != "macOS" || os.Getenv("RUNNER_ARCH") != "ARM64" {
		t.Fatal("requires explicit disposable macOS ARM64 CI lane; no skip")
	}
	binary := os.Getenv("CI_PRODUCER_BINARY")
	expected := os.Getenv("CI_PRODUCER_SHA256")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	sum := sha256.Sum256(raw)
	if !filepath.IsAbs(binary) || hex.EncodeToString(sum[:]) != expected {
		t.Fatal("helper artifact pin mismatch")
	}
	invocation := os.Getenv("CI_PRODUCER_INVOCATION")
	nonce, nonceErr := hex.DecodeString(invocation)
	if nonceErr != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != invocation {
		t.Fatal("fresh invocation identity required")
	}
	report := os.Getenv("CI_PRODUCER_RESULT")
	if !filepath.IsAbs(report) {
		t.Fatal("explicit external result path required")
	}
	if _, e := os.Lstat(report); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("result path must not exist")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	root, err := os.MkdirTemp(cwd, ".ci-producer-")
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	var child *ciChild
	var gateway *ciGateway
	confirmed := false
	// This callback NEVER attempts cleanup after an assertion fails. Persist the
	// failed/unknown outcome, then keep original owners alive until disposable
	// runner teardown. The external observer fails the CI step from this marker.
	t.Cleanup(func() {
		if confirmed {
			return
		}
		data, _ := json.Marshal(map[string]any{"outcome": "failed-unknown", "cleanup": "unconfirmed", "test": t.Name(), "invocation": invocation, "helper_sha256": expected, "fixture_retained": true})
		if e := ciWriteExclusive(report, append(data, '\n')); e != nil {
			fmt.Fprintln(os.Stderr, "CI_PRODUCER_OUTCOME_WRITE_FAILED")
		}
		fmt.Fprintln(os.Stderr, "CI_PRODUCER_FAILED_UNKNOWN: original owners retained; runner teardown is not cleanup proof")
		for {
			time.Sleep(time.Hour)
			runtime.KeepAlive(child)
			runtime.KeepAlive(gateway)
		}
	})
	gateway = newCIGateway(t, root)
	child, err = startCIChild(binary, root, ciBootstrap{Policy: gateway.policyPath, Endpoint: gateway.endpoint, CA: gateway.caDER})
	if err != nil {
		t.Fatal("producer acquisition failed; original ownership retained")
	}
	if err = child.await(func(s Snapshot) bool {
		return s.Standby == "off" && s.Cleanup == "confirmed" && s.Work == "idle" && s.CanRequestOn
	}, 10*time.Second); err != nil {
		t.Fatal("fixture operation refused")
	}
	on, err := child.request("on")
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	if err = child.applied(on, "on"); err != nil {
		t.Fatal("fixture operation refused")
	}
	if err = child.await(func(s Snapshot) bool { return s.Standby == "on" && s.Registration == "authenticated" }, 10*time.Second); err != nil {
		t.Fatal("fixture operation refused")
	}
	generation := gateway.handler.ResidentGeneration(ciHost, ciPrincipal)
	if generation == "" {
		t.Fatal("actual authenticated generation absent")
	}
	for n, mode := range []string{"natural", "cancel"} {
		if gateway.handler.ResidentGeneration(ciHost, ciPrincipal) != generation {
			t.Fatal("producer generation replaced unexpectedly")
		}
		gateway.initialize()
		id := fmt.Sprintf("%032x", n+1)
		start := gateway.tool("rescue_fixed_start", id)
		if start["request_id"] != id {
			t.Fatal("work request correlation absent")
		}
		canceled := false
		deadline := time.Now().Add(60 * time.Second)
		var value map[string]any
		for time.Now().Before(deadline) {
			value = gateway.tool("rescue_fixed_status", id)
			for _, field := range []string{"phase", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
				if value[field] == "unknown" {
					t.Fatal("same-owner cleanup unknown", field)
				}
			}
			if value["error"] != "" {
				t.Fatal("fixed work failure", value["error"])
			}
			output := ciOutput(t, value)
			if mode == "cancel" && !canceled && output == "TETHER_MCP_GUI_BEGIN\n" {
				gateway.tool("rescue_fixed_cancel", id)
				canceled = true
			}
			if value["phase"] == "finished" && value["terminal"] == true {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if value["phase"] != "finished" || value["terminal"] != true || value["command_id"] == "" || value["request_id"] != id {
			t.Fatal("fixed work did not finish", value)
		}
		for _, field := range []string{"command_cleanup", "owner_cleanup", "gateway_cleanup"} {
			if value[field] != "confirmed" {
				t.Fatal("cleanup missing", field)
			}
		}
		expectedOutput := "TETHER_MCP_GUI_BEGIN\nTETHER_MCP_GUI_END\n"
		if mode == "cancel" {
			expectedOutput = "TETHER_MCP_GUI_BEGIN\n"
			if !canceled || value["command_phase"] != "canceled" || value["stop"] != "canceled" {
				t.Fatal("cancel outcome missing", value)
			}
		} else {
			exit, ok := value["exit"].(map[string]any)
			if !ok || exit["known"] != true || exit["code"] != float64(0) || value["command_phase"] != "exited" || value["stop"] != "none" {
				t.Fatal("natural outcome missing", value)
			}
		}
		if ciOutput(t, value) != expectedOutput {
			t.Fatal("real fixed output mismatch")
		}
	}
	off, err := child.request("off")
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	if err = child.applied(off, "off"); err != nil {
		t.Fatal("fixture operation refused")
	}
	if err = child.await(func(s Snapshot) bool {
		return s.Standby == "off" && s.Registration == "disconnected" && s.Work == "idle" && s.Cleanup == "confirmed"
	}, 10*time.Second); err != nil {
		t.Fatal("fixture operation refused")
	}
	select {
	case <-child.waitDone:
		t.Fatal("OFF did not preserve same live producer")
	default:
	}
	if err = child.finish(); err != nil {
		t.Fatal("fixture operation refused")
	}
	if err = gateway.close(); err != nil {
		t.Fatal("gateway original joins", err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal("confirmed fixture disposal failed")
	}
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture remains")
	}
	data, _ := json.Marshal(map[string]any{"outcome": "passed", "cleanup": "confirmed", "test": t.Name(), "invocation": invocation, "helper_sha256": expected, "fixture_retained": false})
	if err = ciWriteExclusive(report, append(data, '\n')); err != nil {
		t.Fatal("fixture operation refused")
	}
	confirmed = true
}
func ciOutput(t *testing.T, value map[string]any) string {
	t.Helper()
	var text strings.Builder
	chunks, ok := value["chunks"].([]any)
	if !ok {
		t.Fatal("output chunks missing")
	}
	for _, raw := range chunks {
		chunk, ok := raw.(map[string]any)
		if !ok || chunk["encoding"] != "base64" || chunk["stream"] != "stdout" {
			t.Fatal("unexpected output chunk")
		}
		encoded, ok := chunk["data"].(string)
		if !ok {
			t.Fatal("output bytes missing")
		}
		data, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			t.Fatal(e)
		}
		text.Write(data)
	}
	return text.String()
}
