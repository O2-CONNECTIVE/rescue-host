package bootstrapauth

import (
	"context"
	"crypto/sha256"
	"io"
	"reflect"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

type imageEvidence struct {
	image  Image
	files  []imageFile
	before []fileState
}

func (s *scope) openImage(ctx context.Context, image Image) error {
	proof := &imageEvidence{image: image}
	for i, node := range image.Nodes {
		if !live(ctx) {
			return ErrDenied
		}
		leaf := i == len(image.Nodes)-1
		f, e := s.api.openFile(ctx, node.Path, image.Volume.Root, leaf)
		if f != nil {
			s.refs = append(s.refs, f)
			proof.files = append(proof.files, f)
		}
		if e != nil || f == nil {
			return denied(e)
		}
		state, e := f.inspect()
		if e != nil || !validFile(s.p, image, node, state, leaf) {
			return denied(e)
		}
		proof.before = append(proof.before, state)
	}
	if e := proof.verify(ctx, s); e != nil {
		return e
	}
	s.images = append(s.images, proof)
	return nil
}
func validFile(p Profile, image Image, node protectedkey.NodePolicy, state fileState, leaf bool) bool {
	if !state.disk || state.reparse || state.path != node.Path || state.owner != node.OwnerSID || !trusted(p, state.owner) || !state.dacl || !safeACL(p, state.acl) || !reflect.DeepEqual(state.acl, node.ACL) || state.filesystem != image.Volume.Filesystem || state.volume != image.Volume.Serial || state.id == [16]byte{} || state.directory == leaf {
		return false
	}
	return !leaf || state.links == 1 && state.size > 0 && state.size <= image.MaxBytes
}
func (p *imageEvidence) verify(ctx context.Context, s *scope) error {
	for i, f := range p.files {
		if !live(ctx) {
			return ErrDenied
		}
		state, e := f.inspect()
		if e != nil || !validFile(s.p, p.image, p.image.Nodes[i], state, i == len(p.files)-1) || !reflect.DeepEqual(state, p.before[i]) {
			return denied(e)
		}
	}
	leaf := p.files[len(p.files)-1]
	if e := leaf.rewind(); e != nil {
		return denied(e)
	}
	hash := sha256.New()
	var total int64
	var buf [32768]byte
	for {
		if !live(ctx) {
			return ErrDenied
		}
		n, e := leaf.Read(buf[:])
		if n < 0 || n > len(buf) {
			return ErrDenied
		}
		if int64(n) > p.image.MaxBytes-total {
			return ErrDenied
		}
		total += int64(n)
		_, _ = hash.Write(buf[:n])
		if e == io.EOF {
			break
		}
		if e != nil || n == 0 {
			return denied(e)
		}
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	if total != p.before[len(p.before)-1].size || digest != p.image.SHA256 {
		return ErrDenied
	}
	for i, f := range p.files {
		if !live(ctx) {
			return ErrDenied
		}
		state, e := f.inspect()
		if e != nil || !reflect.DeepEqual(state, p.before[i]) {
			return denied(e)
		}
	}
	return nil
}
