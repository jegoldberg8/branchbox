// Package jcode locates the host's running jcode server so branch containers
// can join the same mesh instead of each starting an isolated server.
package jcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Server describes one entry of ~/.jcode/servers.json.
type Server struct {
	Name        string `json:"name"`
	Socket      string `json:"socket"`
	DebugSocket string `json:"debug_socket"`
	Version     string `json:"version"`
	PID         int    `json:"pid"`
}

// Home is the host's jcode state directory, bind-mounted into containers so
// sessions, memory and credentials are shared across the mesh.
func Home() (string, error) {
	if dir := os.Getenv("JCODE_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".jcode"), nil
}

// PerMachinePaths are entries of the jcode home that record process state:
// server registries and live PID files. They must not be shared with a
// container.
//
// A container has its own PID namespace, so the host server's PID does not
// exist there. A jcode starting in a container reads the shared registry,
// finds no such process, concludes the server is dead and prunes the entry.
// That empties the registry and breaks agent discovery for every session,
// host and container alike. Each of these gets a private overlay per stack,
// so containers keep their own bookkeeping while still sharing credentials,
// sessions and memory.
var PerMachinePaths = []string{
	"servers.json",
	"active_pids",
	"internal_pids",
	"menubar.pid",
	"menubar.lock",
}

// EnsureStateDir creates a stack's private copies of the per-machine paths.
//
// Each has to exist, and as the right kind of object, before Docker binds it:
// a bind mount over a missing source creates a *directory*, so a registry file
// would appear inside the container as a directory that jcode cannot parse.
func EnsureStateDir(dir string, home string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create the jcode state directory: %w", err)
	}
	for _, name := range PerMachinePaths {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		// Mirror the host's own layout, so each path is created as the kind
		// of object jcode expects rather than guessed from its name.
		dirLike := false
		if info, err := os.Stat(filepath.Join(home, name)); err == nil {
			dirLike = info.IsDir()
		} else {
			dirLike = filepath.Ext(name) == ""
		}
		if dirLike {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("failed to create %s: %w", path, err)
			}
			continue
		}
		body := []byte("")
		if name == "servers.json" {
			// An empty file is not valid JSON; jcode expects an object.
			body = []byte("{}\n")
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return fmt.Errorf("failed to create %s: %w", path, err)
		}
	}
	return nil
}

// Running returns the first registered server whose socket actually exists.
// A stale registry entry is common (the file outlives the process), so the
// socket is what decides.
func Running() (*Server, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(home, "servers.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the jcode server registry: %w", err)
	}
	var registry map[string]Server
	if err := json.Unmarshal(body, &registry); err != nil {
		return nil, fmt.Errorf("failed to parse the jcode server registry: %w", err)
	}
	for _, s := range registry {
		if s.Socket == "" {
			continue
		}
		info, err := os.Stat(s.Socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		srv := s
		return &srv, nil
	}
	return nil, nil
}
