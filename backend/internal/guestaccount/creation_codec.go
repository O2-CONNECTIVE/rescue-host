package guestaccount

import (
	"bytes"
	"encoding/json"
)

func creationRequest(b binding) []byte {
	if b.sid != "" {
		return nil
	}
	w := sidFrameFor(b, "create-disabled")
	w.Protocol = "rescue-host-guest-create/1"
	return encodeSIDFrame(w)
}

type creationWireResult struct {
	Protocol    string          `json:"protocol"`
	Request     json.RawMessage `json:"request"`
	SID         string          `json:"sid"`
	Disabled    bool            `json:"disabled"`
	NameMatches bool            `json:"nameMatches"`
}

func creationResult(request []byte, sid string, disabled, nameMatches bool) []byte {
	if len(request) == 0 || request[len(request)-1] != '\n' {
		return nil
	}
	w := creationWireResult{"rescue-host-guest-created/1", request[:len(request)-1], sid, disabled, nameMatches}
	data, err := json.Marshal(w)
	if err != nil || len(data)+1 > sidFrameLimit {
		return nil
	}
	return append(data, '\n')
}
func decodeCreationResult(p Plan, request, result []byte) (binding, bool, error) {
	if len(request) == 0 || !bytes.Equal(request, creationRequest(p.binding)) || len(result) == 0 || len(result) > sidFrameLimit {
		return binding{}, false, ErrUnknown
	}
	var w creationWireResult
	if json.Unmarshal(result, &w) != nil || w.Protocol != "rescue-host-guest-created/1" || !bytes.Equal(w.Request, request[:len(request)-1]) || !bytes.Equal(creationResult(request, w.SID, w.Disabled, w.NameMatches), result) {
		return binding{}, false, ErrUnknown
	}
	b := p.binding
	b.sid = w.SID
	if !p.validBinding(b) {
		return binding{}, false, ErrUnknown
	}
	return b, w.Disabled && w.NameMatches, nil
}
