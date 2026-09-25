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
	"strings"

	"github.com/jegoldberg8/branchbox/internal/jcode"
	"github.com/jegoldberg8/branchbox/internal/ports"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/worktree"
)

// WorkspaceLink is a stable, project-independent path to the branch checkout.
// The checkout itself is mounted at its host path (see Build), so this symlink
// exists to give scripts and muscle memory one predictable location.
const WorkspaceLink = "/workspace"

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
	Image      string
	Worktree   *worktree.Worktree
	Alloc      ports.Allocation
	HostLogDir string
	JcodeHome  string
	// JcodeStateDir holds this stack's private copies of the jcode paths that
	// record process state; see jcode.PerMachinePaths.
	JcodeStateDir string
	JcodeServer   *jcode.Server
	ExtraMounts   []string
	ContainerName string
	// BuildParallelism caps compiler parallelism inside the container. Zero
	// leaves the toolchain's own default in place.
	BuildParallelism int
	// CPUs is the container's CPU quota. Zero leaves it unlimited.
	CPUs float64
}

// Build renders the configuration for one stack.
func Build(p *profile.Profile, o Options) (*Config, error) {
	if o.Worktree == nil {
		return nil, fmt.Errorf("a worktree is required")
	}
	cfg := &Config{
		Name:  o.ContainerName,
		Image: o.Image,
		// Both checkouts are mounted at their *host* paths. A git worktree's
		// .git is a pointer file containing an absolute path into the main
		// repository's .git/worktrees directory, and that path is recorded on
		// the host. Mounting the checkouts anywhere else leaves git unable to
		// resolve HEAD inside the container.
		WorkspaceFolder: o.Worktree.Path,
		WorkspaceMount:  bind(o.Worktree.Path, o.Worktree.Path, ""),
		ContainerEnv:    map[string]string{},
		RemoteUser:      "dev",
		// The image has no long-running entrypoint of its own; branchbox keeps
		// the container alive and runs services in tmux inside it.
		OverrideCommand: true,
	}

	cfg.RunArgs = []string{"--name", o.ContainerName, "--hostname", o.Worktree.Slug}
	if o.CPUs > 0 {
		// A hard quota, because limiting the toolchain is not enough: each
		// service runs its own compiler, every child inherits GOMAXPROCS
		// rather than sharing it, and the product starves the machine. One
		// stack building must not make another stack's shell unusable.
		cfg.RunArgs = append(cfg.RunArgs, "--cpus", strconv.FormatFloat(o.CPUs, 'f', 2, 64))
	}
	if p.Infra.Compose != "" {
		cfg.RunArgs = append(cfg.RunArgs, "--network", p.Network())
	}
	// Reachable even without a compose network, so a profile with no infra can
	// still talk to services running on the host.
	cfg.RunArgs = append(cfg.RunArgs, "--add-host", "host.docker.internal:host-gateway")

	// The main checkout, at its host path and read-only: the worktree's .git
	// pointer resolves into this directory, so git needs it present, but
	// nothing in the container should write to another branch's checkout.
	if o.Worktree.Path != o.Worktree.MainRepo {
		cfg.Mounts = append(cfg.Mounts, bind(o.Worktree.MainRepo, o.Worktree.MainRepo, "ro"))
	}
	if o.HostLogDir != "" {
		cfg.Mounts = append(cfg.Mounts, bind(o.HostLogDir, LogsDir, ""))
	}
	if o.JcodeHome != "" {
		cfg.Mounts = append(cfg.Mounts, bind(o.JcodeHome, "/home/dev/.jcode", ""))
		// Shadow the host's process bookkeeping with the stack's own. Sharing
		// it lets a container, whose PID namespace does not contain the host
		// server, decide that server is dead and prune it from the registry,
		// which breaks agent discovery everywhere. Credentials, sessions and
		// memory stay shared because only these specific paths are covered.
		if o.JcodeStateDir != "" {
			for _, name := range jcode.PerMachinePaths {
				host := filepath.Join(o.JcodeStateDir, name)
				cfg.Mounts = append(cfg.Mounts, bind(host, "/home/dev/.jcode/"+name, ""))
			}
		}
	}
	if o.JcodeServer != nil {
		// The server's own socket is not mounted directly: on macOS it lives
		// under /var/folders, which Docker Desktop will not share. branchbox
		// relays it into the jcode home instead, and that directory is already
		// mounted above, so the container sees the relay without a second
		// mount.
		cfg.ContainerEnv["JCODE_SOCKET"] = jcode.ContainerSocket
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

	// mise refuses to read a config file from an untrusted path, and the
	// checkout's path is the host's, so it cannot be baked into the image.
	cfg.ContainerEnv["MISE_TRUSTED_CONFIG_PATHS"] = o.Worktree.Path
	cfg.ContainerEnv["BRANCHBOX_PROJECT"] = p.Name
	cfg.ContainerEnv["BRANCHBOX_BRANCH"] = o.Worktree.Branch
	cfg.ContainerEnv["BRANCHBOX_SLUG"] = o.Worktree.Slug

	// Compilers size their parallelism from the CPU count, which on Docker
	// Desktop is the host's while the memory is the VM's much smaller share.
	// Several services building at once then run enough compiler processes to
	// be OOM-killed. Cap the default by the memory the daemon actually has.
	if n := o.BuildParallelism; n > 0 {
		if _, set := cfg.ContainerEnv["GOMAXPROCS"]; !set {
			cfg.ContainerEnv["GOMAXPROCS"] = strconv.Itoa(n)
		}
		if _, set := cfg.ContainerEnv["MAKEFLAGS"]; !set {
			cfg.ContainerEnv["MAKEFLAGS"] = "-j" + strconv.Itoa(n)
		}
	}

	if err := validateMounts(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateMounts rejects a relative bind source. Docker reads a relative
// source as a *volume name*, so an unset path silently becomes a new empty
// volume instead of the host directory that was intended.
func validateMounts(cfg *Config) error {
	all := append([]string{cfg.WorkspaceMount}, cfg.Mounts...)
	for _, m := range all {
		for _, field := range strings.Split(m, ",") {
			src, ok := strings.CutPrefix(field, "source=")
			if !ok {
				continue
			}
			if strings.Contains(m, "type=volume") {
				continue
			}
			if !filepath.IsAbs(src) {
				return fmt.Errorf("bind mount source %q is not an absolute path: %s", src, m)
			}
		}
	}
	return nil
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
