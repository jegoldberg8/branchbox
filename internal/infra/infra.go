// Package infra manages a profile's shared backing services: one Docker
// Compose project per profile, started once and reused by every branch
// container of that profile.
package infra

import (
	"context"
	"fmt"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/docker"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/run"
)

// Project is the compose project name for a profile.
func Project(p *profile.Profile) string { return "branchbox-" + p.Name }

// Up starts the profile's infra and waits for its healthchecks.
func Up(ctx context.Context, p *profile.Profile) error {
	compose := p.ComposePath()
	if compose == "" {
		return fmt.Errorf("profile %s defines no infra", p.Name)
	}
	// The network is created by branchbox, not by compose, so branch
	// containers can join it whether or not infra is running.
	if err := docker.EnsureNetwork(ctx, p.Network()); err != nil {
		return err
	}
	_, err := run.Cmd(ctx, "docker", []string{
		"compose", "-p", Project(p), "-f", compose, "up", "-d", "--wait",
	}, run.Options{Stream: true})
	return err
}

// Down stops the profile's infra. Volumes are kept unless purge is set:
// throwing away every branch's database by accident would be expensive.
func Down(ctx context.Context, p *profile.Profile, purge bool) error {
	compose := p.ComposePath()
	if compose == "" {
		return fmt.Errorf("profile %s defines no infra", p.Name)
	}
	args := []string{"compose", "-p", Project(p), "-f", compose, "down"}
	if purge {
		args = append(args, "--volumes")
	}
	_, err := run.Cmd(ctx, "docker", args, run.Options{Stream: true})
	return err
}

// Status returns compose's own view of the infra services.
func Status(ctx context.Context, p *profile.Profile) (string, error) {
	compose := p.ComposePath()
	if compose == "" {
		return "", fmt.Errorf("profile %s defines no infra", p.Name)
	}
	return run.Cmd(ctx, "docker", []string{
		"compose", "-p", Project(p), "-f", compose, "ps",
		"--format", "table {{.Service}}\t{{.Status}}\t{{.Ports}}",
	}, run.Options{})
}

// Missing returns the profile's infra services that are not currently running.
//
// Checking that *some* container is up is not enough: services stop
// individually, and a stack missing only ClickHouse looks healthy while every
// service that needs it fails to start.
func Missing(ctx context.Context, p *profile.Profile) ([]string, error) {
	if p.ComposePath() == "" {
		return nil, nil
	}
	defined, err := run.Cmd(ctx, "docker", []string{
		"compose", "-p", Project(p), "-f", p.ComposePath(), "config", "--services",
	}, run.Options{})
	if err != nil {
		return nil, err
	}
	up, err := run.Cmd(ctx, "docker", []string{
		"compose", "-p", Project(p), "-f", p.ComposePath(), "ps",
		"--status", "running", "--format", "{{.Service}}",
	}, run.Options{})
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, name := range strings.Fields(up) {
		running[name] = true
	}
	var missing []string
	for _, name := range strings.Fields(defined) {
		if !running[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}
