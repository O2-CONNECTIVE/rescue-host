package guestaccount

import (
	"context"
	"encoding/binary"
	"io"
	"runtime"
)

// The pipe writer owns and erases any internal native-I/O copy after the actual
// operation release. Ordinary io.Writer cannot provide that lifetime contract.
type runtimeSecretWriter interface{ WriteSecret([]byte) (int, error) }

func runtimeCredentialRequest(b binding) []byte {
	w := sidFrameFor(b, "prepare-runtime")
	w.Protocol = "rescue-host-guest-credential/1"
	return encodeSIDFrame(w)
}
func writeRuntimeCredential(ctx context.Context, o *sidRunOwner, w io.WriteCloser, request []byte, secret []uint16) error {
	secure, ok := w.(runtimeSecretWriter)
	if !ok || len(secret) != 37 || secret[36] != 0 || !o.live(ctx) {
		return ErrDenied
	}
	var header [14]byte
	copy(header[:6], "RHGC1\n")
	binary.LittleEndian.PutUint32(header[6:10], uint32(len(request)))
	binary.LittleEndian.PutUint32(header[10:14], 36)
	for _, part := range [][]byte{header[:], request} {
		n, err := w.Write(part)
		if err != nil || n != len(part) || !o.live(ctx) {
			return ErrUnknown
		}
	}
	buffer := make([]byte, 72)
	defer func() { clear(buffer); runtime.KeepAlive(buffer) }()
	for i, c := range secret[:36] {
		binary.LittleEndian.PutUint16(buffer[2*i:], c)
	}
	n, err := secure.WriteSecret(buffer)
	if err != nil || n != len(buffer) || !o.live(ctx) {
		return ErrUnknown
	}
	if err = w.Close(); err != nil || !o.live(ctx) {
		return ErrUnknown
	}
	return nil
}
