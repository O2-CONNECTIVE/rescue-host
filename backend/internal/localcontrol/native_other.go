//go:build !windows

package localcontrol

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"

func NewNative(config Config, wire control.WireContext, coordinator *control.Coordinator) (*Boundary, error) {
	if _, err := NewPolicy(config); err != nil {
		return nil, err
	}
	return nil, ErrClosed
}
