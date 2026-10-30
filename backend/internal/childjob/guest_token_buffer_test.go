package childjob

import (
	"encoding/binary"
	"testing"
)

func tokenGroupBuffer() []byte {
	data := make([]byte, 40)
	binary.LittleEndian.PutUint32(data, 1)
	binary.LittleEndian.PutUint64(data[8:], 0x1018)
	binary.LittleEndian.PutUint32(data[16:], 7)
	copy(data[24:], []byte{1, 2, 0, 0, 0, 0, 0, 5, 32, 0, 0, 0, 33, 2, 0, 0})
	return data
}

// ABI parser regressions are post-implementation checks, not the policy RED.
func TestGuestTokenGroupParserBoundsAllReturnedPointers(t *testing.T) {
	groups, err := guestTokenGroups(tokenGroupBuffer(), 0x1000)
	if err != nil || len(groups) != 1 || groups[0].sid != "S-1-5-32-545" || groups[0].attributes != 7 {
		t.Fatal("bounded native group buffer was not parsed")
	}
	for _, kind := range []string{"before", "header", "beyond", "unaligned", "truncated", "count", "revision", "subcount"} {
		t.Run(kind, func(t *testing.T) {
			data := tokenGroupBuffer()
			switch kind {
			case "before":
				binary.LittleEndian.PutUint64(data[8:], 0xffc)
			case "header":
				binary.LittleEndian.PutUint64(data[8:], 0x1008)
			case "beyond":
				binary.LittleEndian.PutUint64(data[8:], 0x1024)
			case "unaligned":
				binary.LittleEndian.PutUint64(data[8:], 0x1019)
			case "truncated":
				data = data[:39]
			case "count":
				binary.LittleEndian.PutUint32(data, 65)
			case "revision":
				data[24] = 2
			case "subcount":
				data[25] = 16
			}
			if _, err := guestTokenGroups(data, 0x1000); err == nil {
				t.Fatal("invalid native group buffer accepted")
			}
		})
	}
}
func TestGuestTokenPrivilegeParserKeepsDisabledUnknownPresence(t *testing.T) {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data, 1)
	binary.LittleEndian.PutUint64(data[4:], 99)
	privileges, err := guestTokenPrivileges(data, map[uint64]string{1: "SeChangeNotifyPrivilege"})
	if err != nil || len(privileges) != 1 || privileges[0].name != "" || privileges[0].attributes != 0 || privileges[0].luid != 99 {
		t.Fatal("unknown disabled privilege was dropped")
	}
	for _, kind := range []string{"count", "short", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			bad := append([]byte{}, data...)
			switch kind {
			case "count":
				binary.LittleEndian.PutUint32(bad, 65)
			case "short":
				bad = bad[:15]
			case "duplicate":
				bad = append(bad, bad[4:]...)
				binary.LittleEndian.PutUint32(bad, 2)
			}
			if _, err := guestTokenPrivileges(bad, nil); err == nil {
				t.Fatal("invalid native privilege buffer accepted")
			}
		})
	}
}

func TestGuestTokenEmptyRestrictingSIDListHasCountOnlyHeader(t *testing.T) {
	groups, err := guestTokenGroups([]byte{0, 0, 0, 0}, 0x1000)
	if err != nil || groups == nil || len(groups) != 0 {
		t.Fatal("empty restricting SID list required an absent array element")
	}
	if _, err := guestTokenGroups([]byte{1, 0, 0, 0}, 0x1000); err == nil {
		t.Fatal("nonempty SID list accepted without array storage")
	}
}
