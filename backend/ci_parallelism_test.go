package backend

import (
	"runtime"
	"testing"
)

// -cpu is applied by testing after TestMain; observe the actual per-test value.
func TestCILaneParallelism(t *testing.T) {
	value := runtime.GOMAXPROCS(0)
	if value != 1 && value != 2 {
		t.Fatalf("unexpected CI parallelism: %d", value)
	}
	t.Logf("observed_test_GOMAXPROCS=%d", value)
}
