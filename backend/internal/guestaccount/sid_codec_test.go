package guestaccount

import (
	"bytes"
	"testing"
)

func TestSIDCanonicalRequestAndResult(t *testing.T) {
	b := binding{epoch: [32]byte{1}, intent: [32]byte{2}, policy: [32]byte{3}, generation: 7, machine: "S-1-5-21-10-20-30", operator: "S-1-5-21-10-20-30-1001", service: "S-1-5-18", name: "rhg_0123456789abcdef", sid: "S-1-5-21-10-20-30-1002"}
	const request = `{"protocol":"rescue-host-guest-sid/1","operation":"delete","epoch":"0100000000000000000000000000000000000000000000000000000000000000","intent":"0200000000000000000000000000000000000000000000000000000000000000","policy":"0300000000000000000000000000000000000000000000000000000000000000","generation":"7","machine":"S-1-5-21-10-20-30","operator":"S-1-5-21-10-20-30-1001","service":"S-1-5-18","name":"rhg_0123456789abcdef","sid":"S-1-5-21-10-20-30-1002"}` + "\n"
	got := sidRequest(b, deleted)
	if string(got) != request {
		t.Fatalf("別の内部形式: %s", got)
	}
	response := []byte(`{"protocol":"rescue-host-guest-result/1","request":` + request[:len(request)-1] + `,"outcome":"ok"}` + "\n")
	if !validSIDResult(got, response) {
		t.Fatal("正規の結果を拒否")
	}
	for _, changed := range [][]byte{
		bytes.Replace(response, []byte(`"outcome":"ok"`), []byte(`"outcome":true`), 1),
		bytes.Replace(response, []byte(`"outcome":"ok"`), []byte(`"outcome":"ok","unknown":"value"`), 1),
		bytes.Replace(response, []byte(`"generation":"7"`), []byte(`"generation":7`), 1),
		bytes.Replace(response, []byte(`"generation":"7"`), []byte(`"generation":"07"`), 1),
		bytes.Replace(response, []byte(`"sid":"S-1-5-21-10-20-30-1002"`), []byte(`"sid":"S-1-5-21-10-20-30-9999"`), 1),
		append(append([]byte{}, response...), '\n'),
		bytes.Repeat([]byte("x"), sidFrameLimit+1),
	} {
		if validSIDResult(got, changed) {
			t.Fatal("非正規/別要求の結果を受理", string(changed))
		}
	}
}
