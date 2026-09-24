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
	if !strings.Contains(cfg.WorkspaceMount, "source=/host/trees/feature-x") {
		t.Errorf("workspace mount = %q, want the worktree", cfg.WorkspaceMount)
	}
	// Without the main checkout the worktree's .git pointer cannot resolve, so
	// git inside the container would be broken.
	found := false
	for _, m := range cfg.Mounts {
		if strings.Contains(m, "source=/host/repo") && strings.Contains(m, "target="+MainRepoDir) {
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
		if strings.Contains(m, "target="+MainRepoDir) {
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
	want := map[string]bool{"127.0.0.1:3120:3120": true, "127.0.0.1:6120:6120": true}
	if len(cfg.AppPort) != len(want) {
		t.Fatalf("AppPort = %v", cfg.AppPort)
	}
	for _, p := range cfg.AppPort {
		if !want[p] {
			t.Errorf("unexpected published port %q (host and container numbers must match)", p)
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

func TestBuildMountsJcodeSocketsAtAFixedPath(t *testing.T) {
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
	// The host path is a private temp dir; the container path must be stable
	// so the in-container jcode can be pointed at it.
	if !hasMount(cfg, "target="+jcode.ContainerSocketDir+"/jcode.sock") {
		t.Errorf("jcode socket is not mounted at the fixed path: %v", cfg.Mounts)
	}
	if !hasMount(cfg, "target="+jcode.ContainerSocketDir+"/jcode-debug.sock") {
		t.Errorf("jcode debug socket is not mounted: %v", cfg.Mounts)
	}
	if cfg.ContainerEnv["JCODE_SOCKET"] != jcode.ContainerSocketDir+"/jcode.sock" {
		t.Errorf("JCODE_SOCKET = %q", cfg.ContainerEnv["JCODE_SOCKET"])
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
	if back["workspaceFolder"] != WorkspaceDir {
		t.Errorf("workspaceFolder = %v", back["workspaceFolder"])
	}
}
