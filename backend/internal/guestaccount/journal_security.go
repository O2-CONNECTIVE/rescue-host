package guestaccount

type journalACE struct {
	kind, flags uint8
	mask        uint32
	sid         string
}

// exact expected DACL との一致とは別に、危険な expected policy 自体を拒否する。
func journalSecurityAllowed(root journalRootCapability, owner string, aces []journalACE) bool {
	trusted := func(s string) bool {
		return s == root.service || s == root.serviceSID || s == "S-1-5-18" || s == "S-1-5-32-544"
	}
	if !trusted(owner) || aces == nil || len(aces) > 128 {
		return false
	}
	const known = uint32(0xf01f01ff)
	const writes = uint32(0x2 | 0x4 | 0x10 | 0x40 | 0x100 | 0x10000 | 0x40000 | 0x80000 | 0x40000000 | 0x10000000)
	for _, a := range aces {
		if a.kind > 1 || a.flags & ^uint8(0x1f) != 0 || a.mask & ^known != 0 || a.sid == "" {
			return false
		}
		if a.kind == 0 && !trusted(a.sid) && a.mask&writes != 0 {
			return false
		}
	}
	return true
}
