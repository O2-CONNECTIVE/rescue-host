//go:build darwin

package backend

import (
	"fmt"
	"net"
	"syscall"
)

// 自己所有TCPのSYNでMSS等のoptionsを送らず、peerの初期MSSを小さく保つ。
// 接続後のbuffer縮小では、すでに広告した受信windowを取り消せない。
func fixtureBackpressureControl() func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var inner error
		outer := raw.Control(func(fd uintptr) {
			inner = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NOOPT, 1)
			if inner == nil {
				var value int
				value, inner = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NOOPT)
				// Darwinのboolean取得値は正規化した1ではなくflagのbit値。
				if inner == nil && value == 0 {
					inner = fmt.Errorf("自己fixtureの接続前TCP_NOOPTが不一致: %d", value)
				}
			}
		})
		if outer != nil {
			return outer
		}
		return inner
	}
}

func checkFixtureBackpressureProfile(target, peer *net.TCPConn) (string, error) {
	readOption := func(conn *net.TCPConn, option int) (int, error) {
		raw, err := conn.SyscallConn()
		if err != nil {
			return 0, err
		}
		var value int
		var inner error
		outer := raw.Control(func(fd uintptr) {
			value, inner = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, option)
		})
		if outer != nil {
			return 0, outer
		}
		return value, inner
	}
	noopt, err := readOption(target, syscall.TCP_NOOPT)
	if err != nil {
		return "", err
	}
	if noopt == 0 {
		return "", fmt.Errorf("自己fixtureの接続後TCP_NOOPTが不一致: %d", noopt)
	}
	mss, err := readOption(peer, syscall.TCP_MAXSEG)
	if err != nil {
		return "", err
	}
	if mss <= 0 {
		return "", fmt.Errorf("自己fixtureのpeer MSSが無効: %d", mss)
	}
	return fmt.Sprintf("tcp_noopt_enabled=true tcp_noopt_raw=%d peer_mss=%d", noopt, mss), nil
}
