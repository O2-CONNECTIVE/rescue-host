package bootstrap_test

import (
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
)

func TestParseLiteralLaunchArgsAsUntrustedCorrelation(t *testing.T) {
	_, expected, _ := fixture(t)
	expected.Wire.Identity.Epoch = "1"
	args := []string{
		"--supervised-internal",
		"--bootstrap-handle=14",
		"--bootstrap-issuer=" + expected.IssuerInstanceEpoch,
		"--bootstrap-generation=1",
		"--bootstrap-launch-id=launch_1",
		"--bootstrap-session-epoch=1",
		"--bootstrap-policy-digest=" + expected.Wire.Identity.PolicyDigest,
		"--bootstrap-artifact-sha256=" + expected.Wire.Binding.ArtifactSHA256,
		"--bootstrap-channel-nonce=" + expected.Wire.Binding.ChannelNonce,
		"--bootstrap-policy-ref=policy_1",
	}
	got, err := bootstrap.ParseLaunchArgs(args, [3]uintptr{1, 2, 3})
	if err != nil || got.HandleLocator() != 14 || got.Correlation().IssuerInstanceEpoch != expected.IssuerInstanceEpoch || got.Correlation().Wire != expected.Wire {
		t.Fatalf("固定引数の相関値を取り出せない: %v", err)
	}
	args[4] = "--bootstrap-launch-id=changed"
	if got.Correlation().Wire.Identity.LaunchID != "launch_1" {
		t.Fatal("callerのslice変更で解析済み値が変化した")
	}
}

func TestFormatLaunchArgsHasFixedGrammar(t *testing.T) {
	_, expected, _ := fixture(t)
	expected.Wire.Identity.Epoch = "1"
	values := bootstrap.LaunchValues{IssuerInstanceEpoch: expected.IssuerInstanceEpoch, Wire: expected.Wire}
	args, err := bootstrap.FormatLaunchArgs(values, 14, [3]uintptr{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	want := "--supervised-internal --bootstrap-handle=14 --bootstrap-issuer=" + expected.IssuerInstanceEpoch +
		" --bootstrap-generation=1 --bootstrap-launch-id=launch_1 --bootstrap-session-epoch=1 --bootstrap-policy-digest=" + expected.Wire.Identity.PolicyDigest +
		" --bootstrap-artifact-sha256=" + expected.Wire.Binding.ArtifactSHA256 + " --bootstrap-channel-nonce=" + expected.Wire.Binding.ChannelNonce + " --bootstrap-policy-ref=policy_1"
	if strings.Join(args, " ") != want {
		t.Fatal("固定順/field名/非秘密の相関値が変化した")
	}
}

func TestLaunchGrammarRejectsArbitraryArgumentsAndHandleAliases(t *testing.T) {
	for name, change := range map[string]func([]string) []string{
		"missing":              func(a []string) []string { return a[:9] },
		"unknown":              func(a []string) []string { return append(a, "--approve") },
		"duplicate":            func(a []string) []string { return append(a, a[1]) },
		"order":                func(a []string) []string { a[2], a[3] = a[3], a[2]; return a },
		"wrong entry":          func(a []string) []string { a[0] = "--start"; return a },
		"zero handle":          func(a []string) []string { a[1] = "--bootstrap-handle=0"; return a },
		"handle aliases stdio": func(a []string) []string { a[1] = "--bootstrap-handle=2"; return a },
		"signed handle":        func(a []string) []string { a[1] = "--bootstrap-handle=+14"; return a },
		"noncanonical handle":  func(a []string) []string { a[1] = "--bootstrap-handle=014"; return a },
		"overflow handle":      func(a []string) []string { a[1] = "--bootstrap-handle=18446744073709551616"; return a },
		"generation zero":      func(a []string) []string { a[3] = "--bootstrap-generation=0"; return a },
		"issuer uppercase":     func(a []string) []string { a[2] = strings.ToUpper(a[2]); return a },
		"NUL":                  func(a []string) []string { a[4] += "\x00"; return a },
		"nonascii":             func(a []string) []string { a[4] += "あ"; return a },
		"over budget":          func(a []string) []string { a[9] = "--bootstrap-policy-ref=" + strings.Repeat("a", 4096); return a },
	} {
		t.Run(name, func(t *testing.T) {
			_, e, _ := fixture(t)
			a, err := bootstrap.FormatLaunchArgs(bootstrap.LaunchValues{IssuerInstanceEpoch: e.IssuerInstanceEpoch, Wire: e.Wire}, 14, [3]uintptr{1, 2, 3})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bootstrap.ParseLaunchArgs(change(a), [3]uintptr{1, 2, 3}); err != bootstrap.ErrRecord {
				t.Fatalf("不正引数を受理: %v", err)
			}
		})
	}
	_, e, _ := fixture(t)
	for _, stdio := range [][3]uintptr{{}, {1, 1, 3}, {1, 2, 14}} {
		if args, err := bootstrap.FormatLaunchArgs(bootstrap.LaunchValues{IssuerInstanceEpoch: e.IssuerInstanceEpoch, Wire: e.Wire}, 14, stdio); args != nil || err != bootstrap.ErrRecord {
			t.Fatal("無効なstdio/継承handleの重複を受理")
		}
	}
}
