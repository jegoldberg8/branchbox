// Package procompose runs a stack's services under process-compose instead of
// as separate tmux windows.
//
// Both are useful for different things: tmux windows are what you want when
// working *in* one service, process-compose is what you want when watching a
// set of them, because it puts status, restarts and per-process logs in one
// screen. The services and their commands come from the same profile either
// way.
package procompose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jegoldberg8/branchbox/internal/docker"
	"github.com/jegoldberg8/branchbox/internal/profile"
)

// ConfigPath is where the generated config lands inside the container. It sits
// in the log mount so the file is also readable from the host, which makes a
// misbehaving service easy to inspect without attaching.
const ConfigPath = "/var/log/branchbox/process-compose.yaml"

// Port is the process-compose API port inside the container. Fixed rather than
// allocated: it is only ever reached from inside the container, so it cannot
// collide with another stack.
const Port = 8080

// Config is the subset of process-compose's schema branchbox generates.
type Config struct {
	Version   string             `yaml:"version"`
	IsStrict  bool               `yaml:"is_strict"`
	Processes map[string]Process `yaml:"processes"`
}

// Process is one service.
type Process struct {
	Command          string       `yaml:"command"`
	WorkingDir       string       `yaml:"working_dir"`
	Description      string       `yaml:"description,omitempty"`
	LogLocation      string       `yaml:"log_location,omitempty"`
	Availability     Availability `yaml:"availability"`
	ShutDownParams   ShutDown     `yaml:"shutdown"`
	DisableAnsiColor bool         `yaml:"disable_ansi_colors,omitempty"`
}

// Availability controls restarts.
type Availability struct {
	Restart        string `yaml:"restart"`
	BackoffSeconds int    `yaml:"backoff_seconds,omitempty"`
	MaxRestarts    int    `yaml:"max_restarts,omitempty"`
}

// ShutDown gives a service a chance to stop cleanly. Workers in particular
// need to drain rather than be killed outright.
type ShutDown struct {
	Signal  int `yaml:"signal,omitempty"`
	Timeout int `yaml:"timeout_seconds,omitempty"`
}

// Build renders the config for the named services, or for every service in the
// profile when none are named.
func Build(p *profile.Profile, workdir string, services []string) (*Config, error) {
	if len(services) == 0 {
		services = p.ServiceNames()
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("profile %s defines no services", p.Name)
	}
	cfg := &Config{Version: "0.5", IsStrict: true, Processes: map[string]Process{}}
	for _, name := range services {
		command, ok := p.Services[name]
		if !ok {
			return nil, fmt.Errorf("profile %s has no service %q", p.Name, name)
		}
		cfg.Processes[name] = Process{
			Command:    command,
			WorkingDir: workdir,
			// The same path the tmux runner uses, so `branchbox logs` keeps
			// working regardless of which runner started the service.
			LogLocation: "/var/log/branchbox/" + name + ".log",
			Availability: Availability{
				Restart:        "on_failure",
				BackoffSeconds: 2,
				MaxRestarts:    5,
			},
			ShutDownParams: ShutDown{Signal: 15, Timeout: 10},
		}
	}
	return cfg, nil
}

// Write renders the config into the container, via a heredoc rather than a
// bind mount so the file follows the stack rather than the host.
func Write(ctx context.Context, container string, cfg *Config) error {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to encode the process-compose config: %w", err)
	}
	script := fmt.Sprintf("mkdir -p %s && cat > %s <<'BRANCHBOX_EOF'\n%s\nBRANCHBOX_EOF",
		filepath.Dir(ConfigPath), ConfigPath, body)
	if _, err := docker.ExecLogin(ctx, container, script); err != nil {
		return fmt.Errorf("failed to write the process-compose config: %w", err)
	}
	return nil
}

// Names returns the configured process names in a stable order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Processes))
	for name := range c.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UpCommand is the shell command that starts process-compose attached, with
// the TUI on the terminal.
func UpCommand() string {
	return fmt.Sprintf("process-compose -p %d -f %s up", Port, ConfigPath)
}

// AttachCommand reattaches to an already-running project, so closing the TUI
// does not have to mean stopping the services.
func AttachCommand() string {
	return fmt.Sprintf("process-compose attach -p %d", Port)
}

// Attached runs process-compose on the user's terminal, attaching to a running
// project when there is one and starting it otherwise.
func Attached(ctx context.Context, container string, cfg *Config) error {
	if Running(ctx, container) {
		return docker.Attach(container, "bash", "-lc", AttachCommand())
	}
	if err := Write(ctx, container, cfg); err != nil {
		return err
	}
	return docker.Attach(container, "bash", "-lc", UpCommand())
}

// Running reports whether a process-compose project is already up in the
// container.
func Running(ctx context.Context, container string) bool {
	_, err := docker.ExecLogin(ctx, container,
		fmt.Sprintf("process-compose -p %d project state >/dev/null 2>&1", Port))
	return err == nil
}

// Processes lists the project's process names, or nil when no project is
// running. Asking process-compose rather than trusting the generated config
// means a process that was never started does not appear as if it were.
func Processes(ctx context.Context, container string) []string {
	out, err := docker.ExecLogin(ctx, container,
		fmt.Sprintf("process-compose -p %d process list 2>/dev/null", Port))
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Fields(out) {
		names = append(names, line)
	}
	sort.Strings(names)
	return names
}

// Stop shuts the project down, leaving the container itself running.
func Stop(ctx context.Context, container string) error {
	if !Running(ctx, container) {
		return nil
	}
	_, err := docker.ExecLogin(ctx, container, fmt.Sprintf("process-compose -p %d down", Port))
	return err
}

// HostConfigPath is where the generated config is visible on the host, through
// the stack's log mount.
func HostConfigPath(logDir string) string {
	return filepath.Join(logDir, filepath.Base(ConfigPath))
}

// ReadHostConfig reads the generated config from the host side, for tests and
// for inspecting what a stack is actually running.
func ReadHostConfig(logDir string) (*Config, error) {
	body, err := os.ReadFile(HostConfigPath(logDir))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse the generated config: %w", err)
	}
	return &cfg, nil
}
