//go:build !windows

package guestaccount

func newNativeCreationCheck(Plan, *seal) (*creationQueryOwner, error) { return nil, ErrUnconfigured }
func newNativeCreationInspector(Plan, binding, *seal) (*creationQueryOwner, error) {
	return nil, ErrUnconfigured
}
