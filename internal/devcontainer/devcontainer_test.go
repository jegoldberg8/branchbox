package devcontainer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jegoldberg8/branchbox/internal/jcode"
	"github.com/jegoldberg8/branchbox/internal/ports"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/worktree"
)

func testProfile(t *testing.T, extra string) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	body := "name = \"proj\"\nrepo = \"" + dir + "\"\n" + extra
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

func opts(t *testing.T) Options {
	t.Helper()
	return Options{
		Image:         "branchbox:test",
		ContainerName: ContainerName("proj", "feature-x"),
		Worktree: &worktree.Worktree{
			Branch: "feature/x", Slug: "feature-x",
			Path: "/host/trees/feature-x", MainRepo: "/host/repo",
		},
		Alloc:      ports.Allocation{Slot: 2, Ports: map[string]int{"PORT": 3120, "METRICS_PORT": 6120}},
		HostLogDir: "/host/logs",
	}
}

func hasMount(cfg *Config, substr string) bool {
	for _, m := range cfg.Mounts {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return strings.Contains(cfg.WorkspaceMount, substr)
}

func TestBuildMountsMainRepoReadOnly(t *testing.T) {
	cfg, err := Build(testProfile(t, ""), opts(t))
	if err != nil {
		t.Fatal(err)
	}
	// The worktree must appear at its host path: its .git pointer holds an
	// absolute host path into the main repo's .git/worktrees.
	if !strings.Contains(cfg.WorkspaceMount, "source=/host/trees/feature-x,target=/host/trees/feature-x") {
		t.Errorf("workspace mount = %q, want the worktree at its host path", cfg.WorkspaceMount)
	}
	if cfg.WorkspaceFolder != "/host/trees/feature-x" {
		t.Errorf("workspaceFolder = %q", cfg.WorkspaceFolder)
	}
	// Without the main checkout the worktree's .git pointer cannot resolve, so
	// git inside the container would be broken.
	found := false
	for _, m := range cfg.Mounts {
		if strings.Contains(m, "source=/host/repo") && strings.Contains(m, "target=/host/repo") {
			found = true
			if !strings.Contains(m, "readonly") {
				t.Errorf("main repo mount is not read-only: %q", m)
			}
		}
	}
	if !found {
		t.Errorf("main repo is not mounted: %v", cfg.Mounts)
	}
}

func TestBuildSkipsMainRepoMountWhenWorktreeIsTheRepo(t *testing.T) {
	o := opts(t)
	o.Worktree.Path = "/host/repo"
	cfg, err := Build(testProfile(t, ""), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cfg.Mounts {
		if strings.Contains(m, "target=/host/repo") {
			t.Errorf("redundant main repo mount: %q", m)
		}
	}
}

func TestBuildPublishesEveryAllocatedPortOnTheSameNumber(t *testing.T) {
	cfg, err := Build(testProfile(t, ""), opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContainerEnv["PORT"] != "3120" || cfg.ContainerEnv["METRICS_PORT"] != "6120" {
		t.Errorf("ports not in env: %v", cfg.ContainerEnv)
	}
	// The stack's whole slot is published, because services after the first
	// are offset inside it and would otherwise be unreachable from the host.
	for _, want := range []string{"127.0.0.1:3120:3120", "127.0.0.1:6120:6120"} {
		found := false
		for _, got := range cfg.AppPort {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not published: %v", want, cfg.AppPort)
		}
	}
	for _, got := range cfg.AppPort {
		host, container, _ := strings.Cut(strings.TrimPrefix(got, "127.0.0.1:"), ":")
		if host != container {
			t.Errorf("published %q maps different numbers; a printed port must be the one that works", got)
		}
	}
}

func TestBuildCarriesProfileEnvAndIdentity(t *testing.T) {
	p := testProfile(t, "[env]\nTEMPORAL_ADDRESS = \"temporal:7233\"\n")
	cfg, err := Build(p, opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContainerEnv["TEMPORAL_ADDRESS"] != "temporal:7233" {
		t.Errorf("profile env missing: %v", cfg.ContainerEnv)
	}
	if cfg.ContainerEnv["BRANCHBOX_BRANCH"] != "feature/x" {
		t.Errorf("branch identity missing: %v", cfg.ContainerEnv)
	}
}

func TestBuildJoinsTheProfileNetworkOnlyWithInfra(t *testing.T) {
	cfg, err := Build(testProfile(t, ""), opts(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cfg.RunArgs, " "), "--network") {
		t.Errorf("no infra, so no network should be requested: %v", cfg.RunArgs)
	}

	withInfra := testProfile(t, "[infra]\ncompose = \"x.yaml\"\n")
	cfg, err = Build(withInfra, opts(t))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cfg.RunArgs, " ")
	if !strings.Contains(joined, "--network branchbox-proj") {
		t.Errorf("infra profile must join its network: %v", cfg.RunArgs)
	}
	if !strings.Contains(joined, "host.docker.internal:host-gateway") {
		t.Errorf("host gateway missing: %v", cfg.RunArgs)
	}
}

func TestBuildReachesTheJcodeServerThroughTheBridge(t *testing.T) {
	o := opts(t)
	o.JcodeHome = "/host/.jcode"
	o.JcodeServer = &jcode.Server{
		Name:        "desert",
		Socket:      "/private/tmp/xyz/jcode.sock",
		DebugSocket: "/private/tmp/xyz/jcode-debug.sock",
	}
	cfg, err := Build(testProfile(t, ""), o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMount(cfg, "target=/home/dev/.jcode") {
		t.Errorf("jcode home is not shared: %v", cfg.Mounts)
	}
	// The server's own socket lives under /var/folders on macOS, which Docker
	// Desktop will not share, so it must not be mounted directly.
	for _, m := range cfg.Mounts {
		if strings.Contains(m, "/private/tmp/xyz") {
			t.Errorf("the host socket must not be bind-mounted directly: %q", m)
		}
	}
	// It is reached through the relay inside the already-mounted jcode home.
	if cfg.ContainerEnv["JCODE_SOCKET"] != jcode.ContainerSocket {
		t.Errorf("JCODE_SOCKET = %q, want the in-container relay socket", cfg.ContainerEnv["JCODE_SOCKET"])
	}
	// The relay socket must be on the container's own filesystem: a unix
	// socket on a macOS bind mount is unusable from the container side.
	for _, m := range cfg.Mounts {
		if strings.Contains(m, "target="+jcode.ContainerSocket) {
			t.Errorf("the relay socket must not come from a bind mount: %q", m)
		}
	}
}

func TestWriteProducesParsableDevcontainerJSON(t *testing.T) {
	cfg, err := Build(testProfile(t, ""), opts(t))
	if err != nil {
		t.Fatal(err)
	}
	path, err := Write(cfg, filepath.Join(t.TempDir(), "stack"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("generated devcontainer.json does not parse: %v", err)
	}
	if back["workspaceFolder"] != "/host/trees/feature-x" {
		t.Errorf("workspaceFolder = %v", back["workspaceFolder"])
	}
}

// TestBuildAppliesACPUQuota guards the shell-starvation regression at the
// config layer: the quota must reach docker as a run argument, since limiting
// the toolchain inside the container proved insufficient.
func TestBuildAppliesACPUQuota(t *testing.T) {
	o := opts(t)
	o.CPUs = 7
	cfg, err := Build(testProfile(t, ""), o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cfg.RunArgs, " ")
	if !strings.Contains(joined, "--cpus 7.00") {
		t.Errorf("run args do not carry the CPU quota: %v", cfg.RunArgs)
	}

	// Zero means "unlimited", and passing --cpus 0 would be an error.
	o.CPUs = 0
	cfg, err = Build(testProfile(t, ""), o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cfg.RunArgs, " "), "--cpus") {
		t.Errorf("a zero quota must not be passed to docker: %v", cfg.RunArgs)
	}
}

// TestBuildRejectsARelativeMountSource guards a failure that reached a real
// run: an unset state directory produced `source=servers.json`, which docker
// reads as a volume name rather than a path, and the container refused to
// start with an opaque error.
func TestBuildRejectsARelativeMountSource(t *testing.T) {
	o := opts(t)
	o.ExtraMounts = []string{"source=servers.json,target=/home/dev/.jcode/servers.json,type=bind"}
	if _, err := Build(testProfile(t, ""), o); err == nil {
		t.Fatal("expected an error for a relative bind source")
	}

	// Volumes are named, not paths, so they must still be accepted.
	o.ExtraMounts = []string{"source=branchbox-cache,target=/home/dev/.cache,type=volume"}
	if _, err := Build(testProfile(t, ""), o); err != nil {
		t.Errorf("a named volume must be allowed: %v", err)
	}
}

// TestBuildIsolatesPerMachineJcodeState covers the registry-wipe bug: a
// container has its own PID namespace, so sharing servers.json let it decide
// the host server was dead and prune it, breaking agent discovery everywhere.
func TestBuildIsolatesPerMachineJcodeState(t *testing.T) {
	o := opts(t)
	o.JcodeHome = "/host/.jcode"
	o.JcodeStateDir = "/host/state/stack/jcode"
	cfg, err := Build(testProfile(t, ""), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range jcode.PerMachinePaths {
		target := "target=/home/dev/.jcode/" + name
		found := false
		for _, m := range cfg.Mounts {
			if strings.Contains(m, target) && strings.Contains(m, "source=/host/state/stack/jcode/"+name) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not shadowed by the stack's own copy: %v", name, cfg.Mounts)
		}
	}
	// The rest of the jcode home must still be shared, or the container loses
	// credentials, sessions and memory.
	if !hasMount(cfg, "source=/host/.jcode,target=/home/dev/.jcode") {
		t.Error("the jcode home is no longer shared")
	}
}
