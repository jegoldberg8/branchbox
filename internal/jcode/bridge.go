package jcode

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// BridgeDir is where the relayed sockets live. It sits inside the jcode home
// directory because that directory is already shared with containers, whereas
// the server's own socket is not: on macOS it lives under /var/folders, which
// Docker Desktop refuses to bind-mount. Relaying into a shared directory is
// what lets a container attach to the host server at all.
const BridgeDir = "branchbox"

// BridgeSocket is the relayed socket's path on the host.
func BridgeSocket(home string) string {
	return filepath.Join(home, BridgeDir, "jcode.sock")
}

// ContainerBridgeSocket is the same socket as seen from inside a container,
// where the jcode home is mounted at the container user's home.
func ContainerBridgeSocket() string {
	return filepath.Join("/home/dev/.jcode", BridgeDir, "jcode.sock")
}

// EnsureBridge makes sure a relay is running from the shared path to the
// server's real socket, starting one in the background if not. It returns the
// host-side path of the relay.
func EnsureBridge(srv *Server, home, self string) (string, error) {
	path := BridgeSocket(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("failed to create the bridge directory: %w", err)
	}
	if bridgeAlive(path) {
		return path, nil
	}
	// A socket file left behind by a dead relay would block binding.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to clear the stale bridge socket: %w", err)
	}

	cmd := exec.Command(self, "bridge", srv.Socket, path)
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detached: the relay has to outlive the branchbox invocation that
	// started it, since containers use it for as long as they run.
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start the jcode bridge: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return "", fmt.Errorf("failed to detach the jcode bridge: %w", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bridgeAlive(path) {
			return path, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("the jcode bridge did not come up at %s", path)
}

// bridgeAlive reports whether something is accepting connections on path.
func bridgeAlive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RunBridge relays every connection on listen to target. It runs until killed;
// branchbox re-execs itself in this mode rather than depending on socat, which
// is not installed on a stock macOS.
func RunBridge(listen, target string) error {
	if err := os.Remove(listen); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear %s: %w", listen, err)
	}
	ln, err := net.Listen("unix", listen)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listen, err)
	}
	defer ln.Close()
	// The container runs as the same uid, but be explicit rather than relying
	// on the process umask.
	if err := os.Chmod(listen, 0o600); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", listen, err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("bridge accept failed: %w", err)
		}
		go relay(conn, target)
	}
}

func relay(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("unix", target)
	if err != nil {
		// The server went away; dropping the connection lets the client
		// reconnect once it is back.
		return
	}
	defer server.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}
