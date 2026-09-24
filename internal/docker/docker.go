// Package docker wraps the Docker operations branchbox needs: waking the
// daemon, building the base image, managing the per-profile network and
// inspecting or removing branch containers.
package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jegoldberg8/branchbox/internal/run"
)

// BaseImage is the tag of the branchbox base image.
const BaseImage = "branchbox:base"

// EnsureDaemon makes sure the Docker daemon is reachable, starting Docker
// Desktop on macOS if it is merely not running. A stopped daemon is the single
// most common reason `branchbox up` would otherwise fail with an opaque error.
func EnsureDaemon(ctx context.Context) error {
	if err := run.Look("docker"); err != nil {
		return err
	}
	if daemonUp(ctx) {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the Docker daemon is not reachable; start it and retry")
	}
	fmt.Fprintln(os.Stderr, "branchbox: starting Docker Desktop...")
	if _, err := run.Cmd(ctx, "open", []string{"-ga", "Docker"}, run.Options{}); err != nil {
		return fmt.Errorf("failed to start Docker Desktop: %w", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if daemonUp(ctx) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("Docker Desktop did not become ready within 90s")
}

func daemonUp(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	return cmd.Run() == nil
}

// ImageExists reports whether the named image is present locally.
func ImageExists(ctx context.Context, image string) bool {
	_, err := run.Cmd(ctx, "docker", []string{"image", "inspect", image}, run.Options{})
	return err == nil
}

// BuildBase builds the base image from the tool's image directory. The host
// uid and gid are baked in so files created inside the container in the
// bind-mounted worktree are owned by the developer rather than by root.
func BuildBase(ctx context.Context, imageDir, jcodeVersion string) error {
	args := []string{"build", "-t", BaseImage,
		"--build-arg", "USER_UID=" + strconv.Itoa(os.Getuid()),
		"--build-arg", "USER_GID=" + strconv.Itoa(os.Getgid()),
	}
	if jcodeVersion != "" {
		// Pinned so the container's jcode speaks the same protocol as the host
		// server it attaches to.
		args = append(args, "--build-arg", "JCODE_VERSION="+jcodeVersion)
	}
	args = append(args, "-f", imageDir+"/Dockerfile", imageDir)
	_, err := run.Cmd(ctx, "docker", args, run.Options{Stream: true})
	return err
}

// EnsureNetwork creates the profile's shared network if it does not exist. It
// is created by branchbox rather than by compose so that branch containers can
// join it whether or not the infra project is running.
func EnsureNetwork(ctx context.Context, name string) error {
	if _, err := run.Cmd(ctx, "docker", []string{"network", "inspect", name}, run.Options{}); err == nil {
		return nil
	}
	_, err := run.Cmd(ctx, "docker", []string{"network", "create", name}, run.Options{})
	return err
}

// State is a container's lifecycle state: running, exited, or "" when the
// container does not exist.
func State(ctx context.Context, name string) (string, error) {
	out, err := run.Cmd(ctx, "docker", []string{"inspect", "-f", "{{.State.Status}}", name}, run.Options{})
	if err != nil {
		if strings.Contains(out, "No such object") || strings.Contains(err.Error(), "No such object") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Start starts an existing, stopped container.
func Start(ctx context.Context, name string) error {
	_, err := run.Cmd(ctx, "docker", []string{"start", name}, run.Options{})
	return err
}

// Remove force-removes a container, ignoring one that is already gone.
func Remove(ctx context.Context, name string) error {
	out, err := run.Cmd(ctx, "docker", []string{"rm", "-f", name}, run.Options{})
	if err != nil && !strings.Contains(out, "No such container") {
		return err
	}
	return nil
}

// StaleImage reports whether an existing container was created from an image
// other than the current one. The devcontainer CLI reuses a container by
// labels alone, so without this check a rebuilt base image would never reach
// the stacks that were already running.
func StaleImage(ctx context.Context, container, image string) (bool, error) {
	status, err := State(ctx, container)
	if err != nil || status == "" {
		return false, err
	}
	got, err := run.Cmd(ctx, "docker", []string{"inspect", "-f", "{{.Image}}", container}, run.Options{})
	if err != nil {
		return false, err
	}
	want, err := run.Cmd(ctx, "docker", []string{"image", "inspect", "-f", "{{.Id}}", image}, run.Options{})
	if err != nil {
		// No current image means nothing to compare against; the caller
		// builds it before reaching here in the normal path.
		return false, nil
	}
	return strings.TrimSpace(got) != strings.TrimSpace(want), nil
}

// Exec runs a command inside a container and returns its output.
func Exec(ctx context.Context, name string, args []string) (string, error) {
	full := append([]string{"exec", name}, args...)
	return run.Cmd(ctx, "docker", full, run.Options{})
}

// ExecAsRoot runs a command inside a container as root, for the few setup
// steps that touch paths the developer user does not own.
func ExecAsRoot(ctx context.Context, name string, args []string) (string, error) {
	full := append([]string{"exec", "-u", "0", name}, args...)
	return run.Cmd(ctx, "docker", full, run.Options{})
}

// ExecLogin runs a command through a login shell, so mise shims and direnv are
// on PATH exactly as they are for an interactive user.
func ExecLogin(ctx context.Context, name, script string) (string, error) {
	return Exec(ctx, name, []string{"bash", "-lc", script})
}

// Attach runs an interactive command inside a container, wired to the user's
// terminal. The TTY flag is conditional: `docker exec -it` fails outright when
// stdin is a pipe, which is exactly how scripts and CI would call this.
func Attach(name string, args ...string) error {
	flags := "-i"
	if isTerminal(os.Stdin) {
		flags = "-it"
	}
	full := append([]string{"exec", flags, name}, args...)
	return run.Interactive("docker", full...)
}

// isTerminal reports whether f is a real terminal. A character-device check
// alone is not enough: some runners hand over /dev/null, which is a character
// device but makes `docker exec -it` fail outright.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}
