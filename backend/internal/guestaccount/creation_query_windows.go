//go:build windows

package guestaccount

// 既存の query kernel だけを使い、新しい native API や caller callback を足さない。
func newNativeCreationCheck(p Plan, s *seal) (*creationQueryOwner, error) {
	return newCreationCheck(p, s, &accountQueryWindows{})
}
func newNativeCreationInspector(p Plan, b binding, s *seal) (*creationQueryOwner, error) {
	q, e := newNativeAccountQuery(p, b, s)
	if e != nil {
		return nil, e
	}
	return newCreationQueryOwner(q, false)
}
