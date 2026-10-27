//go:build darwin || linux

package backend

import (
	"net"
	"syscall"
)

// 今回所有するfixture listenerの受信bufferだけを小さくする。OS設定を変更しない。
func smallFixtureReceiveBuffer(_, _ string, raw syscall.RawConn) error {
	var inner error
	outer := raw.Control(func(fd uintptr) { inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1024) })
	if outer != nil {
		return outer
	}
	return inner
}

// 読み取るのは今回所有するTCP socketのbuffer値だけである。
func ownedSocketBuffers(conn *net.TCPConn) (send, receive int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var inner error
	outer := raw.Control(func(fd uintptr) {
		send, inner = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
		if inner == nil {
			receive, inner = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		}
	})
	if outer != nil {
		return 0, 0, outer
	}
	return send, receive, inner
}

func fixtureReceiveSize() int { return 1024 }
func fixtureSendSize() int    { return 1024 }
