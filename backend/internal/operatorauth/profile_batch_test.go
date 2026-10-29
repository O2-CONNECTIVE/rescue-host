package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBatchRejectsMissingProfileBeforeAllocating(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	ops.newProfile = nil
	b, err := startBatch(requestContext(t), s, owner, ops)
	if b != nil || !errors.Is(err, ErrUnconfigured) || len(*events) != 0 {
		t.Fatal("unconfigured profile allocated resources", b, err, *events)
	}
}

type memoryBatchProfile struct{ events *[]string }

func (p *memoryBatchProfile) childReader() uintptr { return 63 }
func (p *memoryBatchProfile) launchID() string     { return strings.Repeat("a", 64) }
func (p *memoryBatchProfile) releaseReader(context.Context) error {
	*p.events = append(*p.events, "release-profile-reader")
	return nil
}
func (p *memoryBatchProfile) sendAfterResume(context.Context) error {
	*p.events = append(*p.events, "send-profile")
	return nil
}
func (p *memoryBatchProfile) close(context.Context) error {
	*p.events = append(*p.events, "close-profile")
	return nil
}
