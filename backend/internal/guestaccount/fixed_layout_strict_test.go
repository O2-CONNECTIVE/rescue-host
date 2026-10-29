package guestaccount

import (
	"encoding/json"
	"strings"
	"testing"
)

// generic parserのdepth拒否と、その後の固定schema拒否を分けて確認する。
func TestFixedLayoutStrictDepthCountsContainersAndStillRequiresFixedSchema(t *testing.T) {
	for _, count := range []int{16, 17} {
		for _, scalar := range []string{"0", ""} {
			text := strings.Repeat("[", count) + scalar + strings.Repeat("]", count)
			d := json.NewDecoder(strings.NewReader(text))
			d.UseNumber()
			_, e := fixedLayoutValue(d, 0)
			if (e == nil) != (count == 16) {
				t.Fatal("container depth boundary wrong", count, e)
			}
			if f, e := DecodeFixedLayout([]byte(text)); f != nil || e == nil {
				t.Fatal("generic depth acceptance became fixed layout acceptance")
			}
		}
	}
}
