package procompose

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/docker"
)

// portsPattern matches `process-compose process ports`' output, for example
// "Process bgworker TCP ports: [6111]".
var portsPattern = regexp.MustCompile(`\[([0-9,\s]*)\]`)

// Listeners maps each of a stack's services to the ports it is listening on.
//
// The profile's declared ports say what a stack was *given*; this says what a
// service actually bound, which is what you need when a request is refused.
//
// It asks process-compose rather than reading /proc, because the supervisor
// already knows which PID belongs to which service. Inferring that from
// process ancestry misattributes ports: `go run ./kocmd bgworker` execs a
// binary in the build cache whose command line is a content hash, and matching
// service names against the ancestry text claimed one service's port for
// another.
func Listeners(ctx context.Context, container string, services []string) (map[string][]int, error) {
	out := map[string][]int{}
	for _, name := range services {
		res, err := docker.ExecLogin(ctx, container,
			fmt.Sprintf("process-compose -p %d process ports %s 2>/dev/null", Port, name))
		if err != nil {
			// A service process-compose does not manage simply has no ports
			// to report; that is not an error for the caller.
			continue
		}
		m := portsPattern.FindStringSubmatch(res)
		if m == nil {
			continue
		}
		var ports []int
		// Fields are space separated ("[3110 6110]"), but accept commas too so
		// a formatting change upstream does not silently blank the list.
		for _, field := range strings.FieldsFunc(m[1], func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			p, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			ports = append(ports, p)
		}
		if len(ports) == 0 {
			continue
		}
		sort.Ints(ports)
		out[name] = ports
	}
	return out, nil
}
