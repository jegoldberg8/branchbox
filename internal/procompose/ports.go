package procompose

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/docker"
)

// listenersScript reports every listening TCP port in the container together
// with the command lines of the owning process and its ancestors.
//
// It reads /proc directly rather than using ss or lsof, which are not in the
// image: a service's port is worth showing without asking every project to
// install extra tools. Sockets are matched to processes by inode, which is
// what ss itself does.
//
// Ancestors are included because `go run ./kocmd apiserver` execs a binary in
// the build cache, whose own command line is a content hash with no trace of
// the service. The name only survives in the parent.
const listenersScript = `
for f in /proc/net/tcp /proc/net/tcp6; do
  [ -r "$f" ] || continue
  # State 0A is LISTEN. The local port is hex; convert in the shell because
  # Debian's mawk has no strtonum.
  awk 'NR>1 && $4=="0A" {split($2,a,":"); print a[2], $10}' "$f"
done | sort -u | while read -r hexport inode; do
  port=$((16#$hexport))
  for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
    if ls -l /proc/$pid/fd 2>/dev/null | grep -q "socket:\[$inode\]"; then
      line=""; p=$pid; d=0
      while [ -n "$p" ] && [ "$p" != "0" ] && [ "$d" -lt 6 ]; do
        [ -r "/proc/$p/cmdline" ] || break
        line="$line $(tr '\0' ' ' < /proc/$p/cmdline)"
        # PPid from status, not field 4 of stat: a process name containing a
        # space or parenthesis shifts stat's fields and the walk stops early.
        p=$(awk '/^PPid:/ {print $2}' "/proc/$p/status" 2>/dev/null)
        d=$((d+1))
      done
      printf '%s\t%s\n' "$port" "$line"
      break
    fi
  done
done
`

// Listeners maps each of a stack's services to the ports it is listening on.
//
// The profile's declared ports say what a stack was *given*; this says what a
// service actually bound, which is what you need when a request is refused.
func Listeners(ctx context.Context, container string, services []string) (map[string][]int, error) {
	out, err := docker.ExecLogin(ctx, container, listenersScript)
	if err != nil {
		return nil, fmt.Errorf("failed to read listening ports: %w", err)
	}
	found := map[string]map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		port, cmdline, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			continue
		}
		// Attribute by the service name appearing in the command line, which
		// is how `go run ./kocmd apiserver` and its built binary both resolve
		// to "apiserver".
		for _, name := range services {
			if !strings.Contains(cmdline, name) {
				continue
			}
			if found[name] == nil {
				found[name] = map[int]bool{}
			}
			found[name][n] = true
		}
	}
	out2 := map[string][]int{}
	for name, ports := range found {
		for p := range ports {
			out2[name] = append(out2[name], p)
		}
		sort.Ints(out2[name])
	}
	return out2, nil
}
