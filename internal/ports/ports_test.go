package ports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jegoldberg8/branchbox/internal/profile"
)

func testProfile(t *testing.T) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	body := `repo = "` + dir + `"
name = "proj"
[ports]
PORT = { base = 3100, stride = 10 }
METRICS_PORT = { base = 6100, stride = 10 }
`
	path := filepath.Join(dir, "proj.toml")
	if err := writeFile(path, body); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSlotIsDeterministicAndBounded(t *testing.T) {
	first := Slot("proj", "feature-x")
	for i := 0; i < 5; i++ {
		if got := Slot("proj", "feature-x"); got != first {
			t.Fatalf("Slot is not deterministic: %d then %d", first, got)
		}
	}
	if first < 0 || first >= Slots {
		t.Fatalf("Slot = %d, out of range", first)
	}
	// Different branches should generally land on different slots; identical
	// slots for two names is legal but would make the next assertion flaky, so
	// this only checks that the project name participates in the hash.
	if Slot("proj", "a") == Slot("other", "a") && Slot("proj", "b") == Slot("other", "b") {
		t.Error("project name does not affect slot selection")
	}
}

func TestAllocateSkipsSlotsWithAnyPortInUse(t *testing.T) {
	p := testProfile(t)
	slug := "feature-x"
	start := Slot(p.Name, slug)
	blocked := Resolve(p, start)

	// Only one port of the first slot is taken. The whole slot must be
	// rejected, otherwise a stack would run with interleaved ports.
	taken := blocked["METRICS_PORT"]
	restore := InUse
	InUse = func(port int) bool { return port == taken }
	defer func() { InUse = restore }()

	got, err := Allocate(p, slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slot == start {
		t.Fatalf("Slot = %d, want a slot other than the blocked %d", got.Slot, start)
	}
	if got.Ports["PORT"] != p.Ports["PORT"].Base+got.Slot*10 {
		t.Errorf("PORT = %d, inconsistent with slot %d", got.Ports["PORT"], got.Slot)
	}
}

func TestAllocateFailsWhenEverySlotIsBusy(t *testing.T) {
	p := testProfile(t)
	restore := InUse
	InUse = func(int) bool { return true }
	defer func() { InUse = restore }()

	if _, err := Allocate(p, "feature-x"); err == nil {
		t.Fatal("expected an error when no slot is free")
	}
}

func TestAllocateIsStableForTheSameBranch(t *testing.T) {
	p := testProfile(t)
	restore := InUse
	InUse = func(int) bool { return false }
	defer func() { InUse = restore }()

	a, err := Allocate(p, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Allocate(p, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Slot != b.Slot || a.Ports["PORT"] != b.Ports["PORT"] {
		t.Errorf("allocation is not stable: %+v vs %+v", a, b)
	}
}

func TestDistinctBranchesGetDistinctPorts(t *testing.T) {
	p := testProfile(t)
	used := map[int]bool{}
	restore := InUse
	InUse = func(port int) bool { return used[port] }
	defer func() { InUse = restore }()

	a, err := Allocate(p, "feature-login-fix")
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range a.Ports {
		used[port] = true
	}
	b, err := Allocate(p, "feature-signup-fix")
	if err != nil {
		t.Fatal(err)
	}
	for name, port := range b.Ports {
		if port == a.Ports[name] {
			t.Errorf("%s collides at %d between two live stacks", name, port)
		}
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
