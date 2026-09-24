// Package devcontainer builds the devcontainer.json for one branch stack.
// The file is generated outside the target repository and passed to the
// devcontainer CLI with --override-config, which is what keeps branchbox from
// writing anything into the projects it runs.
package devcontainer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/jegoldberg8/branchbox/internal/jcode"
	"github.com/jegoldberg8/branchbox/internal/ports"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/worktree"
)

// WorkspaceDir is where the branch checkout appears inside the container.
const WorkspaceDir = "/workspace"

// MainRepoDir is where the project's main checkout is mounted, read-only. It
// is not a convenience: a git worktree's .git is a pointer file into the main
// repository's .git/worktrees directory, so git inside the container cannot
// resolve HEAD without it.
const MainRepoDir = "/workspace-main"

// LogsDir is the container side of the host log directory.
const LogsDir = "/var/log/branchbox"

// Config is the subset of devcontainer.json that branchbox generates.
type Config struct {
	Name            string            `json:"name"`
	Image           string            `json:"image"`
	WorkspaceFolder string            `json:"workspaceFolder"`
	WorkspaceMount  string            `json:"workspaceMount"`
	Mounts          []string          `json:"mounts,omitempty"`
	ContainerEnv    map[string]string `json:"containerEnv,omitempty"`
	RunArgs         []string          `json:"runArgs,omitempty"`
	AppPort         []string          `json:"appPort,omitempty"`
	RemoteUser      string            `json:"remoteUser,omitempty"`
	OverrideCommand bool              `json:"overrideCommand"`
}

// Options are the host-side facts needed to render a config.
type Options struct {
	Image         string
	Worktree      *worktree.Worktree
	Alloc         ports.Allocation
	HostLogDir    string
	JcodeHome     string
	JcodeServer   *jcode.Server
	ExtraMounts   []string
	ContainerName string
}

// Build renders the configuration for one stack.
func Build(p *profile.Profile, o Options) (*Config, error) {
	if o.Worktree == nil {
		return nil, fmt.Errorf("a worktree is required")
	}
	cfg := &Config{
		Name:            o.ContainerName,
		Image:           o.Image,
		WorkspaceFolder: WorkspaceDir,
		WorkspaceMount:  bind(o.Worktree.Path, WorkspaceDir, ""),
		ContainerEnv:    map[string]string{},
		RemoteUser:      "dev",
		// The image has no long-running entrypoint of its own; branchbox keeps
		// the container alive and runs services in tmux inside it.
		OverrideCommand: true,
	}

	cfg.RunArgs = []string{"--name", o.ContainerName, "--hostname", o.Worktree.Slug}
	if p.Infra.Compose != "" {
		cfg.RunArgs = append(cfg.RunArgs, "--network", p.Network())
	}
	// Reachable even without a compose network, so a profile with no infra can
	// still talk to services running on the host.
	cfg.RunArgs = append(cfg.RunArgs, "--add-host", "host.docker.internal:host-gateway")

	// The main checkout, read-only: see MainRepoDir.
	if o.Worktree.Path != o.Worktree.MainRepo {
		cfg.Mounts = append(cfg.Mounts, bind(o.Worktree.MainRepo, MainRepoDir, "ro"))
	}
	if o.HostLogDir != "" {
		cfg.Mounts = append(cfg.Mounts, bind(o.HostLogDir, LogsDir, ""))
	}
	if o.JcodeHome != "" {
		cfg.Mounts = append(cfg.Mounts, bind(o.JcodeHome, "/home/dev/.jcode", ""))
	}
	if o.JcodeServer != nil {
		// Mount the sockets individually at a fixed path: the host directory
		// is a private per-user temp directory on macOS, and mounting the
		// whole thing would drag unrelated state along.
		cfg.Mounts = append(cfg.Mounts,
			bind(o.JcodeServer.Socket, filepath.Join(jcode.ContainerSocketDir, "jcode.sock"), ""))
		if o.JcodeServer.DebugSocket != "" {
			cfg.Mounts = append(cfg.Mounts,
				bind(o.JcodeServer.DebugSocket, filepath.Join(jcode.ContainerSocketDir, "jcode-debug.sock"), ""))
		}
		cfg.ContainerEnv["JCODE_SOCKET"] = filepath.Join(jcode.ContainerSocketDir, "jcode.sock")
		cfg.ContainerEnv["JCODE_HOST_SERVER"] = o.JcodeServer.Name
	}
	cfg.Mounts = append(cfg.Mounts, o.ExtraMounts...)

	for k, v := range p.Env {
		cfg.ContainerEnv[k] = v
	}
	for name, port := range o.Alloc.Ports {
		cfg.ContainerEnv[name] = strconv.Itoa(port)
		// Publish on the same number inside and out, so a port printed by
		// branchbox is the port that works from the host.
		cfg.AppPort = append(cfg.AppPort, fmt.Sprintf("127.0.0.1:%d:%d", port, port))
	}
	sort.Strings(cfg.AppPort)

	cfg.ContainerEnv["BRANCHBOX_PROJECT"] = p.Name
	cfg.ContainerEnv["BRANCHBOX_BRANCH"] = o.Worktree.Branch
	cfg.ContainerEnv["BRANCHBOX_SLUG"] = o.Worktree.Slug
	return cfg, nil
}

// Write renders the config to disk and returns its path.
func Write(cfg *Config, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dir, err)
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode devcontainer config: %w", err)
	}
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", path, err)
	}
	return path, nil
}

// ContainerName is the docker name for a stack. It doubles as the lookup key
// for ps, logs, shell and down.
func ContainerName(project, slug string) string {
	return "branchbox-" + project + "-" + slug
}

func bind(source, target, opts string) string {
	m := "source=" + source + ",target=" + target + ",type=bind"
	if opts == "ro" {
		m += ",readonly"
	}
	return m
}
