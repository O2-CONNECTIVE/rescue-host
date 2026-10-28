package serviceprovision

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 公開配置値だけの固定 JSON witness。decoder自身で期待入力を生成しません。
const validJSON = `{"protocol":"rescue-service-provisioning/1","profile":{"keys":{"version":"1","processUserSid":"S-1-5-21-1-2-3-1000","serviceSid":"S-1-5-80-1-2-3-4-5","operatorSid":"S-1-5-21-1-2-3-1001","guestSid":"S-1-5-21-1-2-3-1002","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"provisioning":{"reference":"synthetic","protectedAt":"2026-01-01T00:00:00Z"},"catalog":{"fixed_host":{"path":"C:\\host-key","fingerprint":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","nodes":[{"path":"C:\\","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]},{"path":"C:\\host-key","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]}]}}},"serviceName":"SyntheticService","supervisorImage":{"path":"C:\\supervisor.exe","sha256":"0100000000000000000000000000000000000000000000000000000000000000","maxBytes":"1024","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"nodes":[{"path":"C:\\","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]},{"path":"C:\\supervisor.exe","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]}]},"childImage":{"path":"C:\\child.exe","sha256":"0100000000000000000000000000000000000000000000000000000000000000","maxBytes":"1024","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"nodes":[{"path":"C:\\","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]},{"path":"C:\\child.exe","ownerSid":"S-1-5-21-1-2-3-1000","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]}]}},"endpoint":"\\\\.\\pipe\\rescue-service-SyntheticService","serviceCommandLine":"\"C:\\supervisor.exe\"","serviceSecurity":{"ownerSid":"S-1-5-21-1-2-3-1000","protected":true,"acl":[{"type":"0","flags":"0","mask":"983551","sid":"S-1-5-80-1-2-3-4-5"}]},"processSecurity":{"ownerSid":"S-1-5-21-1-2-3-1000","protected":true,"acl":[{"type":"0","flags":"0","mask":"2097151","sid":"S-1-5-80-1-2-3-4-5"}]},"readyCleanupTimeoutNs":"1000000000","budgets":{"startupNs":"2000000000","cleanupNs":"2000000000","observeNs":"1000000"},"policyRef":"fixed_policy","pollIntervalNs":"1000000","operationTimeoutNs":"1000000000","stdioCleanupTimeoutNs":"1000000000","stderrBytes":"4096","backend":{"operationMode":"restricted","allowedKeys":["AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea"],"maximumDurationNs":"20000000000","leaseDurationNs":"1000000000","maxChannels":"1","maxHandshakes":"2","maxTransports":"3"},"hostKeyRef":"fixed_host","limits":{"frameBytes":"16384","frames":"10","cleanupTimeoutNs":"1000000000"}}`

func TestDecodeRecordReturnsExactPublicFixedValues(t *testing.T) {
	got, err := DecodeRecord([]byte(validJSON))
	if err != nil || !reflect.DeepEqual(got, validRecord()) {
		t.Fatalf("固定配置値を損失なく復元しませんでした: %v", err)
	}
	withLF, err := DecodeRecord(append([]byte(validJSON), '\n'))
	if err != nil || !reflect.DeepEqual(withLF, got) {
		t.Fatal("許可した単一LFで値が変わりました")
	}
}

func TestDecodeRecordRejectsAmbiguousOrExtraStructure(t *testing.T) {
	cases := map[string][]byte{
		"duplicate root":   []byte(strings.Replace(validJSON, `{"protocol":`, `{"protocol":"rescue-service-provisioning/1","protocol":`, 1)),
		"duplicate nested": []byte(strings.Replace(validJSON, `"serviceName":"SyntheticService"`, `"serviceName":"SyntheticService","serviceName":"SyntheticService"`, 1)),
		"unknown root":     []byte(strings.Replace(validJSON, `{"protocol":`, `{"grant":{},"protocol":`, 1)),
		"unknown nested":   []byte(strings.Replace(validJSON, `"serviceName":"SyntheticService"`, `"serviceName":"SyntheticService","identity":{}`, 1)),
		"case mismatch":    []byte(strings.Replace(validJSON, `"protocol":`, `"Protocol":`, 1)),
		"null ACL":         []byte(strings.Replace(validJSON, `"acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-80-1-2-3-4-5"}]`, `"acl":null`, 1)),
		"leading space":    []byte(" " + validJSON),
		"trailing space":   []byte(validJSON + " "),
		"extra LF":         []byte(validJSON + "\n\n"),
		"two objects":      []byte(validJSON + validJSON),
		"BOM":              append([]byte{0xef, 0xbb, 0xbf}, []byte(validJSON)...),
		"invalid UTF8":     bytes.ReplaceAll([]byte(validJSON), []byte(".exe"), []byte{0xff, '.', 'e', 'x', 'e'}),
		"size":             []byte("{" + strings.Repeat(" ", RecordBytes) + validJSON[1:]),
		"depth":            []byte(strings.Replace(validJSON, `{"protocol":`, `{"extra":`+strings.Repeat("[", 17)+"0"+strings.Repeat("]", 17)+`,"protocol":`, 1)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if r, e := DecodeRecord(data); e != ErrRecord || !reflect.DeepEqual(r, Record{}) {
				t.Fatal("曖昧または余分な構造を値へ変換しました")
			}
		})
	}
}

func TestDecodeRecordRejectsUnpairedUnicodeEscapes(t *testing.T) {
	for _, escape := range []string{`\ud800`, `\udc00`} {
		data := bytes.ReplaceAll([]byte(validJSON), []byte(".exe"), []byte(escape+".exe"))
		if _, e := DecodeRecord(data); e != ErrRecord {
			t.Fatal("不正 Unicode escape を置換してpathの値を受理しました")
		}
	}
	data := bytes.ReplaceAll([]byte(validJSON), []byte(".exe"), []byte(`\ud83d\ude00.exe`))
	if _, e := DecodeRecord(data); e != nil {
		t.Fatalf("正しいsurrogate pairの公開pathを拒否しました: %v", e)
	}
}

func TestDecodeRecordRejectsNoncanonicalOrOverflowingValues(t *testing.T) {
	for name, change := range map[string][2]string{
		"numeric JSON":               {"\"stderrBytes\":\"4096\"", "\"stderrBytes\":4096"},
		"leading zero":               {`"startupNs":"2000000000"`, `"startupNs":"02000000000"`},
		"plus":                       {`"startupNs":"2000000000"`, `"startupNs":"+2000000000"`},
		"negative":                   {`"startupNs":"2000000000"`, `"startupNs":"-1"`},
		"exponent":                   {`"startupNs":"2000000000"`, `"startupNs":"2e9"`},
		"u64 overflow":               {`"stderrBytes":"4096"`, `"stderrBytes":"18446744073709551616"`},
		"duration overflow":          {`"operationTimeoutNs":"1000000000"`, `"operationTimeoutNs":"9223372036854775808"`},
		"zero duration":              {`"pollIntervalNs":"1000000"`, `"pollIntervalNs":"0"`},
		"ACE type overflow":          {`"type":"0"`, `"type":"256"`},
		"ACL mask overflow":          {`"mask":"2032127"`, `"mask":"4294967296"`},
		"version":                    {`"version":"1"`, `"version":"01"`},
		"serial":                     {`"serial":"7"`, `"serial":"0"`},
		"hash uppercase":             {`0100000000000000000000000000000000000000000000000000000000000000`, `0A00000000000000000000000000000000000000000000000000000000000000`},
		"time offset":                {`2026-01-01T00:00:00Z`, `2026-01-01T00:00:00+00:00`},
		"time noncanonical fraction": {`2026-01-01T00:00:00Z`, `2026-01-01T00:00:00.000Z`},
		"bad public key":             {`AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea`, `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea`},
		"secret unknown field":       {`"policyRef":"fixed_policy"`, `"privateKey":"forbidden_fixture_marker","policyRef":"fixed_policy"`},
		"removed required":           {`"stderrBytes":"4096",`, ``},
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(strings.Replace(validJSON, change[0], change[1], 1))
			if bytes.Equal(data, []byte(validJSON)) {
				t.Fatal("変異入力が作れませんでした")
			}
			if r, e := DecodeRecord(data); e != ErrRecord || !reflect.DeepEqual(r, Record{}) {
				t.Fatal("非canonical/overflow/未知値を受理しました")
			}
		})
	}
}

func TestDecodeRecordAllowsRepresentableBoundariesWithoutDefaults(t *testing.T) {
	data := strings.Replace(validJSON, `"stderrBytes":"4096"`, `"stderrBytes":"18446744073709551615"`, 1)
	data = strings.Replace(data, `"startupNs":"2000000000"`, `"startupNs":"9223372036854775807"`, 1)
	data = strings.Replace(data, `"frames":"10"`, `"frames":"2147483647"`, 1)
	got, e := DecodeRecord([]byte(data))
	if e != nil || got.StderrBytes != ^uint64(0) || got.Budgets.Startup != time.Duration(1<<63-1) || got.Limits.Frames != 2147483647 {
		t.Fatalf("表現可能な明示境界を拒否またはdefaultへ変えました: %v", e)
	}
}

func TestDecodedRecordDoesNotAliasInputOrOtherDecodes(t *testing.T) {
	data := []byte(validJSON)
	one, e := DecodeRecord(data)
	if e != nil {
		t.Fatal(e)
	}
	two, e := DecodeRecord(data)
	if e != nil {
		t.Fatal(e)
	}
	for i := range data {
		data[i] = 0
	}
	one.Profile.SupervisorImage.Nodes[0].ACL[0].SID = "changed"
	key := one.Profile.Keys.Catalog["fixed_host"]
	key.Nodes[0].ACL[0].SID = "changed"
	one.Profile.Keys.Catalog["fixed_host"] = key
	one.Backend.AllowedKeys[0] = "changed"
	if !reflect.DeepEqual(two, validRecord()) {
		t.Fatal("別decoderまたは入力bufferの変更が値に伝播しました")
	}
}
