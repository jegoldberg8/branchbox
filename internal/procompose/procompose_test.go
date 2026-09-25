package procompose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jegoldberg8/branchbox/internal/profile"
)

func testProfile(t *testing.T) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	body := `name = "proj"
repo = "` + dir + `"
[services]
api = "go run ./cmd/api"
worker = "go run ./cmd/worker"
`
	path := filepath.Join(dir, "proj.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildUsesTheProfileCommands(t *testing.T) {
	p := testProfile(t)
	cfg, err := Build(p, "/work", []string{"api"}, map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Processes) != 1 {
		t.Fatalf("processes = %v, want only the requested service", cfg.Names())
	}
	got := cfg.Processes["api"]
	if got.Command != "go run ./cmd/api" {
		t.Errorf("Command = %q, want the profile's", got.Command)
	}
	if got.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q", got.WorkingDir)
	}
	// The same log path the tmux runner writes, so `branchbox logs` works
	// whichever runner started the service.
	if !strings.HasSuffix(got.LogLocation, "/api.log") {
		t.Errorf("LogLocation = %q, want the shared log path", got.LogLocation)
	}
	// A worker killed outright mid-task is worse than one given time to drain.
	if got.ShutDownParams.Signal != 15 || got.ShutDownParams.Timeout <= 0 {
		t.Errorf("shutdown = %+v, want a TERM with a grace period", got.ShutDownParams)
	}
}

func TestBuildDefaultsToEveryService(t *testing.T) {
	cfg, err := Build(testProfile(t), "/work", nil, map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Processes) != 2 {
		t.Errorf("processes = %v, want all of the profile's services", cfg.Names())
	}
}

func TestBuildRejectsAnUnknownService(t *testing.T) {
	if _, err := Build(testProfile(t), "/work", []string{"nope"}, map[string]int{}); err == nil {
		t.Fatal("expected an error for a service the profile does not define")
	}
}

func TestBuildRejectsAProfileWithNoServices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(path, []byte("name=\"e\"\nrepo=\""+dir+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(p, "/work", nil, map[string]int{}); err == nil {
		t.Fatal("expected an error when there is nothing to run")
	}
}

// TestConfigIsValidYAML matters because the config is written into the
// container through a heredoc: a quoting mistake would surface as a
// process-compose parse error rather than a Go error.
func TestConfigIsValidYAML(t *testing.T) {
	cfg, err := Build(testProfile(t), "/work", nil, map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "BRANCHBOX_EOF") {
		t.Error("config contains the heredoc terminator, which would truncate the file")
	}
	var back Config
	if err := yaml.Unmarshal(body, &back); err != nil {
		t.Fatalf("generated config does not parse: %v", err)
	}
	if len(back.Processes) != len(cfg.Processes) {
		t.Errorf("round trip lost processes: %v", back.Processes)
	}
	if back.Version == "" {
		t.Error("version is required by process-compose")
	}
}

func TestCommandsTargetTheSameProject(t *testing.T) {
	// up and attach must agree on the port, or attaching would start a second
	// project instead of joining the first.
	if !strings.Contains(UpCommand(), ConfigPath) {
		t.Errorf("UpCommand does not reference the config: %s", UpCommand())
	}
	for _, cmd := range []string{UpCommand(), AttachCommand()} {
		if !strings.Contains(cmd, "8080") {
			t.Errorf("%q does not pin the project port", cmd)
		}
	}
}

func TestHostConfigPathIsInTheLogMount(t *testing.T) {
	// The config has to be readable from the host, which only works because it
	// is written into the directory branchbox mounts for logs.
	if got := HostConfigPath("/host/logs"); got != "/host/logs/process-compose.yaml" {
		t.Errorf("HostConfigPath = %q", got)
	}
	if filepath.Dir(ConfigPath) != "/var/log/branchbox" {
		t.Errorf("ConfigPath = %q, want it inside the log mount", ConfigPath)
	}
}

// TestStatusHealthyRequiresNoRestarts encodes why `ps` reported a stack as
// "running" while bgworker was crash-looping: a service that dies and is
// restarted every few seconds reads as "Running" at almost any instant.
func TestStatusHealthyRequiresNoRestarts(t *testing.T) {
	cases := []struct {
		name string
		st   Status
		want bool
	}{
		{"running cleanly", Status{State: "Running"}, true},
		{"crash looping", Status{State: "Running", Restarts: 4}, false},
		{"exited", Status{State: "Completed", ExitCode: 1}, false},
		{"never started", Status{State: "Pending"}, false},
	}
	for _, c := range cases {
		if got := c.st.Healthy(); got != c.want {
			t.Errorf("%s: Healthy() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBuildGivesEachServiceItsOwnPorts covers a bug that cost real debugging
// time: every service inherited the same METRICS_PORT, so only the first to
// bind got it and the rest logged "address already in use" while appearing to
// run normally.
func TestBuildGivesEachServiceItsOwnPorts(t *testing.T) {
	dir := t.TempDir()
	body := `name = "proj"
repo = "` + dir + `"
[ports]
METRICS_PORT = { base = 6100, stride = 10 }
[services]
api = "run api"
worker = "run worker"
`
	path := filepath.Join(dir, "proj.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// This stack was allocated slot 1, so its ports start at 6110.
	cfg, err := Build(p, "/work", []string{"api", "worker"}, map[string]int{"METRICS_PORT": 6110})
	if err != nil {
		t.Fatal(err)
	}
	// The first service keeps the stack's own port, so the documented number
	// still means something.
	if env := cfg.Processes["api"].Environment; len(env) != 0 {
		t.Errorf("the first service should keep the stack's port, got %v", env)
	}
	worker := cfg.Processes["worker"].Environment
	if len(worker) != 1 || worker[0] != "METRICS_PORT=6111" {
		t.Errorf("worker env = %v, want METRICS_PORT=6111", worker)
	}
}

func TestBuildDoesNotOffsetPastTheStackSlot(t *testing.T) {
	dir := t.TempDir()
	// A stride of 1 leaves no room: the next port belongs to another stack.
	body := `name = "proj"
repo = "` + dir + `"
[ports]
PORT = { base = 3100, stride = 1 }
[services]
a = "run a"
b = "run b"
`
	path := filepath.Join(dir, "proj.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Build(p, "/work", []string{"a", "b"}, map[string]int{"PORT": 3101})
	if err != nil {
		t.Fatal(err)
	}
	if env := cfg.Processes["b"].Environment; len(env) != 0 {
		t.Errorf("env = %v, want no offset that would take another stack's port", env)
	}
}
