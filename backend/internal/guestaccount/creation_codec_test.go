package guestaccount

import (
	"bytes"
	"testing"
)

func TestCreationResultRejectsAnotherIntentEvenWhenRequestAndReplyAgree(t *testing.T) {
	p := testPlan(t)
	other := p.binding
	other.intent[0]++
	request := creationRequest(other)
	result := creationResult(request, p.binding.machine+"-2002", true, true)
	if _, _, err := decodeCreationResult(p, request, result); err == nil {
		t.Fatal("別intentの自己一致結果を作成来歴にした")
	}
}

func TestCreationResultCanonicalShapeAndStagingFlags(t *testing.T) {
	p := testPlan(t)
	request := creationRequest(p.binding)
	sid := p.binding.machine + "-2002"
	good := creationResult(request, sid, true, true)
	if b, ready, err := decodeCreationResult(p, request, good); err != nil || !ready || b.sid != sid {
		t.Fatal(b, ready, err)
	}
	for _, changed := range [][]byte{
		bytes.Replace(good, []byte(`"disabled":true`), []byte(`"disabled":"true"`), 1),
		bytes.Replace(good, []byte(`"disabled":true`), []byte(`"disabled":true,"unknown":true`), 1),
		bytes.Replace(good, []byte(`"disabled":true`), []byte(`"disabled":true,"disabled":true`), 1),
		bytes.Replace(good, []byte(`"nameMatches":true`), nil, 1),
		bytes.Replace(good, []byte(`-2002"`), []byte(`-02002"`), 1),
		append(append([]byte{}, good...), good...),
		bytes.Repeat([]byte("x"), sidFrameLimit+1),
	} {
		if _, _, err := decodeCreationResult(p, request, changed); err == nil {
			t.Fatal("不正な来歴結果を受理", string(changed))
		}
	}
	for _, flags := range [][2]bool{{false, true}, {true, false}, {false, false}} {
		b, ready, err := decodeCreationResult(p, request, creationResult(request, sid, flags[0], flags[1]))
		if err != nil || ready || b.sid != sid {
			t.Fatal("未準備の確定SIDを捨てた", b, ready, err)
		}
	}
}
