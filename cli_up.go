package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jegoldberg8/branchbox/internal/devcontainer"
	"github.com/jegoldberg8/branchbox/internal/docker"
	"github.com/jegoldberg8/branchbox/internal/infra"
	"github.com/jegoldberg8/branchbox/internal/jcode"
	"github.com/jegoldberg8/branchbox/internal/paths"
	"github.com/jegoldberg8/branchbox/internal/ports"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/run"
	"github.com/jegoldberg8/branchbox/internal/state"
	"github.com/jegoldberg8/branchbox/internal/tmux"
	"github.com/jegoldberg8/branchbox/internal/worktree"
)

func cmdUp(ctx context.Context, args []string) error {
	args, opt := flags(args, "rebuild")
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: branchbox up [project] <branch> [service ...]")
	}
	branch, services := rest[0], rest[1:]
	for _, s := range services {
		if _, ok := p.Services[s]; !ok {
			return fmt.Errorf("profile %s has no service %q (known: %s)",
				p.Name, s, strings.Join(p.ServiceNames(), ", "))
		}
	}

	if err := docker.EnsureDaemon(ctx); err != nil {
		return err
	}
	if err := run.Look("devcontainer"); err != nil {
		return fmt.Errorf("%w; install it with `npm i -g @devcontainers/cli`", err)
	}
	if opt["rebuild"] || !docker.ImageExists(ctx, docker.BaseImage) {
		if err := buildImage(ctx, e); err != nil {
			return err
		}
	}

	wt, err := worktree.Ensure(p, branch)
	if err != nil {
		return err
	}
	head, err := worktree.Head(wt.Path)
	if err != nil {
		return err
	}

	if p.ComposePath() != "" {
		if err := docker.EnsureNetwork(ctx, p.Network()); err != nil {
			return err
		}
		if !infra.Running(ctx, p) {
			fmt.Fprintf(os.Stderr,
				"branchbox: %s infra is not running; start it with `branchbox infra up %s`\n", p.Name, p.Name)
		}
	}

	container := devcontainer.ContainerName(p.Name, wt.Slug)
	alloc, err := allocatePorts(ctx, e, p, wt.Slug, container)
	if err != nil {
		return err
	}

	logDir := paths.LogDir(e.state, p.Name, wt.Slug)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", logDir, err)
	}

	srv, err := jcode.Running()
	if err != nil {
		return err
	}
	if srv == nil {
		// Not fatal: a stack is still useful without the mesh, but silently
		// losing it would be confusing.
		fmt.Fprintln(os.Stderr, "branchbox: no running jcode server found; the container will not join the mesh")
	}
	jcodeHome, err := jcode.Home()
	if err != nil {
		return err
	}

	cfg, err := devcontainer.Build(p, devcontainer.Options{
		Image:         docker.BaseImage,
		Worktree:      wt,
		Alloc:         alloc,
		HostLogDir:    logDir,
		JcodeHome:     jcodeHome,
		JcodeServer:   srv,
		ContainerName: container,
		ExtraMounts:   cacheMounts(p, wt.Slug),
	})
	if err != nil {
		return err
	}
	cfgDir := e.stackDir(p.Name, wt.Slug)
	cfgPath, err := devcontainer.Write(cfg, cfgDir)
	if err != nil {
		return err
	}

	fmt.Printf("branchbox: %s %s (%s) -> %s\n", p.Name, branch, head, wt.Path)
	if _, err := run.Cmd(ctx, "devcontainer", []string{
		"up",
		"--workspace-folder", wt.Path,
		"--override-config", cfgPath,
		"--id-label", "branchbox.project=" + p.Name,
		"--id-label", "branchbox.slug=" + wt.Slug,
	}, run.Options{Stream: true}); err != nil {
		return err
	}

	if len(p.Setup.Steps) > 0 {
		fmt.Println("branchbox: running setup steps")
		for _, step := range p.Setup.Steps {
			if out, err := docker.ExecLogin(ctx, container, step); err != nil {
				return fmt.Errorf("setup step %q failed: %w\n%s", step, err, out)
			}
		}
	}

	st := &state.Stack{
		Project: p.Name, Branch: branch, Slug: wt.Slug, Worktree: wt.Path,
		ContainerName: container, ConfigDir: cfgDir, LogDir: logDir,
		Image: docker.BaseImage, Slot: alloc.Slot, Ports: alloc.Ports,
		StartedAt: time.Now(),
	}
	for _, name := range services {
		if err := tmux.StartService(ctx, container, name, p.Services[name]); err != nil {
			return err
		}
		st.Services = append(st.Services, name)
		fmt.Printf("branchbox: started %s (log: %s)\n", name, filepath.Join(logDir, name+".log"))
	}
	if err := e.store.Save(st); err != nil {
		return err
	}

	for _, name := range alloc.Names() {
		fmt.Printf("branchbox: %s=%d (published on the host)\n", name, alloc.Ports[name])
	}
	fmt.Printf("branchbox: shell with `branchbox shell %s`\n", wt.Slug)
	return nil
}

// allocatePorts keeps a restarted stack on the ports it already had. Without
// this the stack's own published ports would look "in use" and every restart
// would drift to a new slot.
func allocatePorts(ctx context.Context, e *env, p *profile.Profile, slug, container string) (ports.Allocation, error) {
	if prev, err := e.store.Load(p.Name, slug); err == nil && len(prev.Ports) > 0 {
		if st, err := docker.State(ctx, container); err == nil && st != "" {
			return ports.Allocation{Slot: prev.Slot, Ports: prev.Ports}, nil
		}
	}
	return ports.Allocate(p, slug)
}

// cacheMounts gives each branch its own build cache volume. Sharing one cache
// between branches would serialize their builds and thrash on differing
// dependency sets.
func cacheMounts(p *profile.Profile, slug string) []string {
	vol := "branchbox-cache-" + p.Name + "-" + slug
	return []string{
		"source=" + vol + ",target=/home/dev/.cache,type=volume",
		"source=" + vol + "-tools,target=/home/dev/.local/share/mise,type=volume",
	}
}

func buildImage(ctx context.Context, e *env) error {
	// Pin the container's jcode to the host's version: the container binary
	// attaches to the host server's socket, so the two must speak the same
	// protocol.
	version := hostJcodeVersion(ctx)
	fmt.Printf("branchbox: building the base image (jcode %s)\n", version)
	return docker.BuildBase(ctx, filepath.Join(e.tool, "image"), version)
}

func hostJcodeVersion(ctx context.Context) string {
	out, err := run.Cmd(ctx, "jcode", []string{"version"}, run.Options{})
	if err != nil {
		return "latest"
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "semver" {
			return fields[1]
		}
	}
	return "latest"
}

func cmdRun(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: branchbox run [project] <branch> <service ...>")
	}
	st, err := e.store.Resolve(p.Name, rest[0])
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, st); err != nil {
		return err
	}
	for _, name := range rest[1:] {
		command, ok := p.Services[name]
		if !ok {
			return fmt.Errorf("profile %s has no service %q (known: %s)",
				p.Name, name, strings.Join(p.ServiceNames(), ", "))
		}
		if err := tmux.StartService(ctx, st.ContainerName, name, command); err != nil {
			return err
		}
		if !contains(st.Services, name) {
			st.Services = append(st.Services, name)
		}
		fmt.Printf("branchbox: started %s\n", name)
	}
	sort.Strings(st.Services)
	return e.store.Save(st)
}

// ensureRunning restarts a stopped container, so a reboot or a `docker stop`
// does not force the user to recreate the stack.
func ensureRunning(ctx context.Context, st *state.Stack) error {
	status, err := docker.State(ctx, st.ContainerName)
	if err != nil {
		return err
	}
	switch status {
	case "running":
		return nil
	case "":
		return fmt.Errorf("container %s no longer exists; run `branchbox up %s`", st.ContainerName, st.Branch)
	default:
		return docker.Start(ctx, st.ContainerName)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
