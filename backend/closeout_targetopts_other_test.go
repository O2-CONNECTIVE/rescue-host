//go:build !darwin

package backend

import (
	"net"
	"syscall"
)

func fixtureBackpressureControl() func(string, string, syscall.RawConn) error { return nil }

func checkFixtureBackpressureProfile(_, _ *net.TCPConn) (string, error) {
	return "tcp_options=platform_default", nil
}
