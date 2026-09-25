package docker

import (
	"context"
	"runtime"
	"testing"
)

// TestBuildParallelismFitsTheDaemonMemory guards the OOM this exists to
// prevent: on Docker Desktop the container sees the host's CPU count but a
// fraction of its memory, so a toolchain sizing itself by CPU starts far more
// compilers than the VM can hold.
func TestBuildParallelismFitsTheDaemonMemory(t *testing.T) {
	n := BuildParallelism(context.Background())
	if n == 0 {
		t.Skip("no reachable Docker daemon to size against")
	}
	if n < 1 {
		t.Fatalf("BuildParallelism = %d, want at least 1", n)
	}
	if n > runtime.NumCPU() {
		t.Errorf("BuildParallelism = %d, more than the %d CPUs available", n, runtime.NumCPU())
	}
	// The whole point is to stay under what the daemon can hold. Recompute the
	// budget from the same figure the function used.
	total := daemonMemory(context.Background())
	if total > 0 {
		if want := int(total / 2 / memPerBuildProcess); want >= 1 && want <= runtime.NumCPU() && n != want {
			t.Errorf("BuildParallelism = %d, want %d for a %d byte daemon", n, want, total)
		}
	}
}

func TestBuildParallelismIsStable(t *testing.T) {
	ctx := context.Background()
	first := BuildParallelism(ctx)
	if first == 0 {
		t.Skip("no reachable Docker daemon")
	}
	// A value that drifts between calls would give two stacks of the same
	// project different build settings for no reason.
	for i := 0; i < 3; i++ {
		if got := BuildParallelism(ctx); got != first {
			t.Fatalf("BuildParallelism is not stable: %d then %d", first, got)
		}
	}
}
