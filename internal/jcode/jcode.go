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
