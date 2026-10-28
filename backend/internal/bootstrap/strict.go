package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// field 名の照合は大小文字を区別する。encoding/json の曖昧照合に委ねない。
func strictRecord(record []byte) bool {
	if len(record) < 3 || len(record) > RecordBytes || record[0] != '{' || record[len(record)-2] != '}' || record[len(record)-1] != '\n' || bytes.IndexByte(record[:len(record)-1], '\n') >= 0 || !utf8.Valid(record) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(record))
	d.UseNumber()
	v, err := readValue(d, 1)
	if err != nil {
		return false
	}
	if _, err = d.Token(); err != io.EOF {
		return false
	}
	r, ok := exact(v, "protocol", "issuerInstanceEpoch", "grant", "binding", "policyRef", "backend", "hostKeyRef", "stdio")
	if !ok {
		return false
	}
	g, ok := exact(r["grant"], "generation", "identity", "keySetDigest", "guestBoundary", "hardDeadline", "backendLeaseCeilingNs")
	if !ok {
		return false
	}
	if _, ok = exact(g["identity"], "launchId", "epoch", "policyDigest"); !ok {
		return false
	}
	if _, ok = exact(r["binding"], "artifactSha256", "channelNonce"); !ok {
		return false
	}
	if _, ok = exact(r["backend"], "operationMode", "allowedKeys", "maximumDurationNs", "leaseDurationNs", "maxChannels", "maxHandshakes", "maxTransports"); !ok {
		return false
	}
	_, ok = exact(r["stdio"], "frames", "cleanupTimeoutNs")
	return ok
}

func exact(v any, names ...string) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, ok = m[name]; !ok {
			return nil, false
		}
	}
	return m, true
}

func readValue(d *json.Decoder, depth int) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, ErrRecord
	}
	delim, container := t.(json.Delim)
	if !container {
		return t, nil
	}
	if depth > 8 {
		return nil, ErrRecord
	}
	switch delim {
	case '{':
		m := make(map[string]any)
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, ErrRecord
			}
			name, ok := k.(string)
			if !ok {
				return nil, ErrRecord
			}
			if _, exists := m[name]; exists {
				return nil, ErrRecord
			}
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrRecord
		}
		return m, nil
	case '[':
		values := make([]any, 0)
		for d.More() {
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrRecord
		}
		return values, nil
	}
	return nil, ErrRecord
}
