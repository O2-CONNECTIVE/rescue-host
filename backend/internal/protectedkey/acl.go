package protectedkey

import (
	"strconv"
	"strings"
)

func safeACL(p Profile, acl []ACE, leaf bool) bool {
	if acl == nil {
		return false
	}
	for _, ace := range acl {
		if ace.Type > 1 || ace.Flags & ^uint8(0x1f) != 0 || ace.Mask & ^uint32(0xf01f01ff) != 0 || !validSID(ace.SID) {
			return false
		}
		// INHERIT_ONLY は当該 node で実効になりません。各子を別々に検査します。
		if ace.Type == 1 || ace.Flags&8 != 0 || trusted(p, ace.SID) {
			continue
		}
		mask := expandGeneric(ace.Mask)
		dangerous := uint32(0x000d0156) // 書込・子作成・EA・属性・子削除・削除・DACL/owner 変更
		if leaf {
			dangerous |= 0x21 // データ読取と実行
		}
		// この方針では別の DENY で危険な ALLOW を相殺して受理しません。
		if mask&dangerous != 0 {
			return false
		}
	}
	return true
}

func expandGeneric(mask uint32) uint32 {
	expanded := mask & 0x001f01ff
	for _, pair := range []struct{ generic, rights uint32 }{
		{0x80000000, 0x00120089}, {0x40000000, 0x00120116},
		{0x20000000, 0x001200a0}, {0x10000000, 0x001f01ff},
	} {
		if mask&pair.generic != 0 {
			expanded |= pair.rights
		}
	}
	return expanded
}

func validSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 4 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, part := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, err := strconv.ParseUint(part, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}
