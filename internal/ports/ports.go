// Package ports allocates a deterministic, collision-free set of host ports
// for each branch stack. Determinism matters: the same branch gets the same
// ports every time, so a stack can be reattached, scripted against and
// documented without consulting a registry.
package ports

import (
	"fmt"
	"hash/fnv"
	"net"
	"sort"
	"strconv"

	"github.com/jegoldberg8/branchbox/internal/profile"
)

// Slots is the number of distinct port slots. Branch stacks beyond this many
// would collide, so the allocator probes for a free slot instead.
const Slots = 16

// Slot derives a starting slot from the project and branch slug. It is only a
// starting point: Allocate probes forward when a slot is already taken.
func Slot(project, slug string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(project + "/" + slug))
	return int(h.Sum32() % Slots)
}

// Allocation is the resolved environment for one branch stack.
type Allocation struct {
	Slot  int
	Ports map[string]int
}

// Names returns the allocated port variables in a stable order.
func (a Allocation) Names() []string {
	names := make([]string, 0, len(a.Ports))
	for name := range a.Ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// InUse reports whether a TCP port on the host is already bound. It is a
// package variable so tests can exercise the probing logic without opening
// real sockets.
var InUse = func(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

// Allocate resolves every port in the profile for the given branch slug,
// probing forward from the hashed slot until every port in the set is free.
// Ports are allocated as a set: a slot is only accepted if all of its ports
// are available, so one stack's ports never interleave with another's.
func Allocate(p *profile.Profile, slug string) (Allocation, error) {
	start := Slot(p.Name, slug)
	for i := 0; i < Slots; i++ {
		slot := (start + i) % Slots
		candidate := Resolve(p, slot)
		free := true
		for _, port := range candidate {
			if InUse(port) {
				free = false
				break
			}
		}
		if free {
			return Allocation{Slot: slot, Ports: candidate}, nil
		}
	}
	return Allocation{}, fmt.Errorf("no free port slot for %s/%s: all %d slots are in use", p.Name, slug, Slots)
}

// Resolve computes the ports for a specific slot without checking
// availability.
func Resolve(p *profile.Profile, slot int) map[string]int {
	out := make(map[string]int, len(p.Ports))
	for name, spec := range p.Ports {
		out[name] = spec.Base + slot*spec.Stride
	}
	return out
}
