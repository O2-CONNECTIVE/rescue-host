package bootstrapauth

import (
	"context"
	"io"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

type group struct {
	sid        string
	attributes uint32
}
type identity struct {
	user   string
	kind   uint32
	groups []group
}
type pipeState struct {
	kind, flags, mode, access uint32
	owner                     string
	protected                 bool
	acl                       []protectedkey.ACE
	pid                       uint32
}
type serviceState struct{ pid, state uint32 }
type processState struct {
	pid      uint32
	creation uint64
	alive    bool
	path     string
	identity identity
}
type owned interface{ close() error }
type serviceHandle interface {
	owned
	status() (serviceState, error)
}
type processHandle interface {
	owned
	inspect() (processState, error)
}
type imageFile interface {
	owned
	io.Reader
	rewind() error
	inspect() (fileState, error)
}
type fileState struct {
	path, owner, filesystem        string
	acl                            []protectedkey.ACE
	volume                         uint64
	id                             [16]byte
	links                          uint32
	size                           int64
	disk, directory, reparse, dacl bool
}

// kernel は非公開の試験境界です。公開 caller は native 実装を差し替えられません。
type kernel interface {
	threadID() uint32
	cleanThread() error
	principal(uintptr) (identity, error)
	currentPID() uint32
	currentImage() (string, error)
	inspectPipe(uintptr) (pipeState, error)
	openService(string) (serviceHandle, error)
	openProcess(uint32) (processHandle, error)
	openFile(context.Context, string, string, bool) (imageFile, error)
}
