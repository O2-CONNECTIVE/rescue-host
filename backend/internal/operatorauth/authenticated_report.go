package operatorauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// 内部reportだけの上限。公開requestの上限/schemaは変更しない。
const authenticatedReportBytes = 32768

type actualSubjectWire struct {
	User           string `json:"user"`
	Authentication string `json:"authentication"`
	Session        string `json:"session"`
	Integrity      string `json:"integrity"`
	Kind           string `json:"kind"`
	Level          string `json:"level"`
	Restricted     bool   `json:"restricted"`
	AppContainer   bool   `json:"appContainer"`
}

func subjectWire(p tokenProjection) actualSubjectWire {
	return actualSubjectWire{p.user, strconv.FormatUint(p.authentication, 10), strconv.FormatUint(uint64(p.session), 10), strconv.FormatUint(uint64(p.integrity), 10), strconv.FormatUint(uint64(p.kind), 10), strconv.FormatUint(uint64(p.level), 10), p.restricted, p.appContainer}
}
func encodeAuthenticatedReport(frame []byte, launchID string, policy SIDPolicy, actual tokenProjection) ([]byte, error) {
	if !profileLaunchID(launchID) || !policy.accepts(actual) {
		return nil, ErrDenied
	}
	if _, err := servicecontrol.DecodeUntrustedFrame(frame); err != nil {
		return nil, ErrDenied
	}
	w := struct {
		Version  string            `json:"version"`
		Type     string            `json:"type"`
		LaunchID string            `json:"launchId"`
		Actual   actualSubjectWire `json:"actualSubject"`
		Frame    []byte            `json:"publicFrame"`
	}{"2", "authenticated-operator", launchID, subjectWire(actual), frame}
	b, err := json.Marshal(w)
	if err != nil || len(b)+1 > authenticatedReportBytes {
		return nil, ErrDenied
	}
	return append(b, '\n'), nil
}

// decodeは保護pipe/EOF/exit/join証明を作らない。Collectorの同launch経路専用。
func decodeAuthenticatedReport(frame []byte, launchID string, policy SIDPolicy) ([]byte, tokenProjection, error) {
	root, err := internalFrameObject(frame, authenticatedReportBytes)
	if err != nil || !profileLaunchID(launchID) || !profileKeys(root, "version", "type", "launchId", "actualSubject", "publicFrame") || root["version"] != "2" || root["type"] != "authenticated-operator" || root["launchId"] != launchID {
		return nil, tokenProjection{}, ErrDenied
	}
	m, ok := root["actualSubject"].(map[string]any)
	if !ok {
		return nil, tokenProjection{}, ErrDenied
	}
	actual, err := decodeActualSubject(m)
	if err != nil || !policy.accepts(actual) {
		return nil, tokenProjection{}, ErrDenied
	}
	encoded, ok := root["publicFrame"].(string)
	if !ok {
		return nil, tokenProjection{}, ErrDenied
	}
	b, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(b) != encoded {
		return nil, tokenProjection{}, ErrDenied
	}
	if _, err = servicecontrol.DecodeUntrustedFrame(b); err != nil {
		return nil, tokenProjection{}, ErrDenied
	}
	return b, actual, nil
}
func internalFrameObject(frame []byte, limit int) (map[string]any, error) {
	if len(frame) == 0 || len(frame) > limit || !utf8.Valid(frame) || frame[len(frame)-1] != '\n' || bytes.ContainsAny(frame[:len(frame)-1], "\r\n") {
		return nil, ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(frame[:len(frame)-1]))
	root, err := profileObject(d, 0)
	if err != nil {
		return nil, ErrDenied
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrDenied
	}
	return root, nil
}
func decodeActualSubject(m map[string]any) (tokenProjection, error) {
	if !profileKeys(m, "user", "authentication", "session", "integrity", "kind", "level", "restricted", "appContainer") {
		return tokenProjection{}, ErrDenied
	}
	user, ok := m["user"].(string)
	if !ok || !reportSIDValid(user) {
		return tokenProjection{}, ErrDenied
	}
	a, ok := profileNumber(m["authentication"], 64)
	if !ok || a == 0 {
		return tokenProjection{}, ErrDenied
	}
	s, ok := profileNumber(m["session"], 32)
	if !ok {
		return tokenProjection{}, ErrDenied
	}
	i, ok := profileNumber(m["integrity"], 32)
	if !ok || i == 0 {
		return tokenProjection{}, ErrDenied
	}
	k, ok := profileNumber(m["kind"], 32)
	if !ok {
		return tokenProjection{}, ErrDenied
	}
	l, ok := profileNumber(m["level"], 32)
	if !ok {
		return tokenProjection{}, ErrDenied
	}
	r, ok := m["restricted"].(bool)
	if !ok {
		return tokenProjection{}, ErrDenied
	}
	c, ok := m["appContainer"].(bool)
	if !ok {
		return tokenProjection{}, ErrDenied
	}
	return tokenProjection{user: user, authentication: a, session: uint32(s), integrity: uint32(i), kind: uint32(k), level: uint32(l), restricted: r, appContainer: c}, nil
}
