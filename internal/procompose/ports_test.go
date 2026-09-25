package procompose

import (
	"strconv"
	"strings"
	"testing"
)

// parsePorts mirrors the extraction in Listeners, so the parsing can be tested
// without a container.
func parsePorts(out string) []int {
	m := portsPattern.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	var ports []int
	for _, field := range strings.FieldsFunc(m[1], func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		p, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		ports = append(ports, p)
	}
	return ports
}

// TestParsePortsHandlesTheActualFormat guards a bug that silently blanked the
// port column: process-compose separates ports with spaces, and splitting on
// commas produced one unparsable field, so a service holding two ports
// appeared to hold none.
func TestParsePortsHandlesTheActualFormat(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []int
	}{
		{"single", "Process apiserver TCP ports: [3110]", []int{3110}},
		{"space separated", "Process apiserver TCP ports: [3110 6110]", []int{3110, 6110}},
		{"comma separated", "Process x TCP ports: [3110,6110]", []int{3110, 6110}},
		{"none", "Process x TCP ports: []", nil},
		{"unexpected output", "some error", nil},
	}
	for _, c := range cases {
		got := parsePorts(c.out)
		if len(got) != len(c.want) {
			t.Errorf("%s: parsed %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: parsed %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}
