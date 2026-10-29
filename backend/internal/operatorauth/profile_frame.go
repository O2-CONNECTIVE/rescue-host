package operatorauth

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// encodeHelperProfileFrameは値の形式だけを確認し、正規producerの証拠を生成しない。
func encodeHelperProfileFrame(launchID string, p helperProfile) ([]byte, error) {
	type operatorWire struct {
		User           string `json:"user"`
		Authentication string `json:"authentication"`
		Session        string `json:"session"`
		Integrity      string `json:"integrity"`
		Kind           string `json:"kind"`
		Level          string `json:"level"`
		Restricted     bool   `json:"restricted"`
		AppContainer   bool   `json:"appContainer"`
	}
	w := struct {
		Version   string       `json:"version"`
		Type      string       `json:"type"`
		LaunchID  string       `json:"launchId"`
		Operator  operatorWire `json:"operator"`
		GuestUser string       `json:"guestUser"`
	}{"1", "helper-profile", launchID, operatorWire{p.operator.user, strconv.FormatUint(p.operator.authentication, 10), strconv.FormatUint(uint64(p.operator.session), 10), strconv.FormatUint(uint64(p.operator.integrity), 10), strconv.FormatUint(uint64(p.operator.kind), 10), strconv.FormatUint(uint64(p.operator.level), 10), p.operator.restricted, p.operator.appContainer}, p.guest.user}
	frame, err := json.Marshal(w)
	if err != nil {
		return nil, ErrDenied
	}
	frame = append(frame, '\n')
	decoded, err := decodeHelperProfileFrame(frame, launchID)
	if err != nil || decoded != p {
		return nil, ErrDenied
	}
	return frame, nil
}

// decodeHelperProfileFrameは完全frameの純粋な構造検査だけを行う。
// EOF・正規producer・同一launchのOS bindingは配送側の責任である。
func decodeHelperProfileFrame(frame []byte, expected string) (helperProfile, error) {
	var out helperProfile
	if len(frame) == 0 || len(frame) > 16384 || !utf8.Valid(frame) || frame[len(frame)-1] != '\n' || bytes.ContainsAny(frame[:len(frame)-1], "\r\n") || !profileLaunchID(expected) {
		return out, ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(frame[:len(frame)-1]))
	root, err := profileObject(d, 0)
	if err != nil {
		return out, ErrDenied
	}
	if _, err = d.Token(); err != io.EOF {
		return out, ErrDenied
	}
	if !profileKeys(root, "version", "type", "launchId", "operator", "guestUser") || root["version"] != "1" || root["type"] != "helper-profile" || root["launchId"] != expected {
		return out, ErrDenied
	}
	guest, ok := root["guestUser"].(string)
	if !ok || !reportSIDValid(guest) {
		return out, ErrDenied
	}
	op, ok := root["operator"].(map[string]any)
	if !ok || !profileKeys(op, "user", "authentication", "session", "integrity", "kind", "level", "restricted", "appContainer") {
		return out, ErrDenied
	}
	user, ok := op["user"].(string)
	if !ok || !reportSIDValid(user) || user == guest {
		return out, ErrDenied
	}
	a, ok := profileNumber(op["authentication"], 64)
	if !ok || a == 0 {
		return out, ErrDenied
	}
	s, ok := profileNumber(op["session"], 32)
	if !ok {
		return out, ErrDenied
	}
	i, ok := profileNumber(op["integrity"], 32)
	if !ok || i == 0 {
		return out, ErrDenied
	}
	k, ok := profileNumber(op["kind"], 32)
	if !ok || k != 2 {
		return out, ErrDenied
	}
	l, ok := profileNumber(op["level"], 32)
	if !ok || (l != 1 && l != 2) {
		return out, ErrDenied
	}
	r, ok := op["restricted"].(bool)
	if !ok || r {
		return out, ErrDenied
	}
	c, ok := op["appContainer"].(bool)
	if !ok || c {
		return out, ErrDenied
	}
	out.operator = tokenProjection{user: user, authentication: a, session: uint32(s), integrity: uint32(i), kind: uint32(k), level: uint32(l), restricted: r, appContainer: c}
	out.guest.user = guest
	return out, nil
}

func profileObject(d *json.Decoder, depth int) (map[string]any, error) {
	if depth > 1 {
		return nil, ErrDenied
	}
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrDenied
	}
	m := make(map[string]any)
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return nil, ErrDenied
		}
		name, ok := key.(string)
		if !ok {
			return nil, ErrDenied
		}
		if _, exists := m[name]; exists {
			return nil, ErrDenied
		}
		if name == "operator" && depth == 0 {
			v, e := profileObject(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[name] = v
			continue
		}
		v, e := d.Token()
		if e != nil {
			return nil, ErrDenied
		}
		switch v.(type) {
		case string, bool:
			m[name] = v
		default:
			return nil, ErrDenied
		}
	}
	t, err = d.Token()
	if err != nil || t != json.Delim('}') {
		return nil, ErrDenied
	}
	return m, nil
}
func profileKeys(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
func profileNumber(v any, bits int) (uint64, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, bits)
	return n, err == nil && strconv.FormatUint(n, 10) == s
}
func profileLaunchID(s string) bool {
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return false
	}
	return strings.Trim(s, "0") != ""
}
