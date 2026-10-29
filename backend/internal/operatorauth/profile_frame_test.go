package operatorauth

import (
	"strings"
	"testing"
)

func TestProfileFrameRequiresMatchingLaunchAndStrictFields(t *testing.T) {
	id := strings.Repeat("a", 64)
	frame := `{"version":"1","type":"helper-profile","launchId":"` + id + `","operator":{"user":"S-1-5-21-1","authentication":"7","session":"3","integrity":"8192","kind":"2","level":"1","restricted":false,"appContainer":false},"guestUser":"S-1-5-21-2"}` + "\n"
	p, err := decodeHelperProfileFrame([]byte(frame), id)
	if err != nil || p.operator.authentication != 7 || p.guest.user != "S-1-5-21-2" {
		t.Fatalf("profile=%+v err=%v", p, err)
	}
	for _, invalid := range []string{
		strings.Replace(frame, `"version":"1"`, `"version":"1","version":"1"`, 1),
		strings.Replace(frame, `"user":"S-1-5-21-1"`, `"user":"S-1-5-21-1","user":"S-1-5-21-1"`, 1),
		strings.Replace(frame, `"user":"S-1-5-21-1"`, `"user":"S-1-5-21-1","\u0075ser":"S-1-5-21-1"`, 1),
		strings.Replace(frame, `"session":"3"`, `"session":"03"`, 1),
		strings.Replace(frame, `"session":"3"`, `"session":3`, 1),
		strings.Replace(frame, `"session":"3"`, `"session":null`, 1),
		strings.Replace(frame, `"session":"3"`, `"session":"+3"`, 1),
		strings.Replace(frame, `"authentication":"7"`, `"authentication":"18446744073709551616"`, 1),
		strings.Replace(frame, `"restricted":false`, `"restricted":true`, 1),
		strings.Replace(frame, `"appContainer":false`, `"appContainer":true`, 1),
		strings.Replace(frame, `"kind":"2"`, `"kind":"1"`, 1),
		strings.Replace(frame, `"level":"1"`, `"level":"3"`, 1),
		strings.Replace(frame, `S-1-5-21-2`, `S-1-05-21-2`, 1),
		strings.Replace(frame, `"guestUser":"S-1-5-21-2"`, `"guestUser":"S-1-5-21-1"`, 1),
		strings.Replace(frame, `"guestUser":`, `"unknown":1,"guestUser":`, 1),
		strings.TrimSuffix(frame, "\n"),
		frame + "\n",
		strings.TrimSuffix(frame, "\n") + "{}\n",
		strings.TrimSuffix(frame, "\n") + "\r\n",
		string([]byte{0xff}) + frame,
		strings.Repeat(" ", 16384) + frame,
	} {
		if _, err := decodeHelperProfileFrame([]byte(invalid), id); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
	if _, err := decodeHelperProfileFrame([]byte(frame), strings.Repeat("b", 64)); err == nil {
		t.Fatal("old launch accepted")
	}
	for _, invalidID := range []string{strings.Repeat("0", 64), strings.Repeat("A", 64)} {
		if _, err := decodeHelperProfileFrame([]byte(frame), invalidID); err == nil {
			t.Fatal("invalid launch id accepted")
		}
	}
}
