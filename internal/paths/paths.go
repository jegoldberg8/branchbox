// Package paths resolves where branchbox keeps its own files. The tool
// directory holds versioned assets (image, templates, shipped profiles); the
// state directory holds everything mutable, so the git repo stays clean.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// ToolDir is the branchbox checkout: image/, templates/, profiles/.
func ToolDir() (string, error) {
	if dir := os.Getenv("BRANCHBOX_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to locate the branchbox binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the branchbox binary: %w", err)
	}
	// Walk up from the binary looking for the assets. This covers both an
	// installed binary sitting next to them and `go run` from the checkout.
	dir := filepath.Dir(exe)
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "image", "Dockerfile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("cannot find branchbox assets near %s; set BRANCHBOX_HOME", exe)
}

// StateDir holds per-stack state, logs and user profiles.
func StateDir() (string, error) {
	if dir := os.Getenv("BRANCHBOX_STATE"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "branchbox"), nil
}

// LogDir is where a stack's service logs land on the host, so they are
// readable without attaching to the container.
func LogDir(stateDir, project, slug string) string {
	return filepath.Join(stateDir, "logs", project, slug)
}
