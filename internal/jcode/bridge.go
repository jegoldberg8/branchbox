package jcode

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ContainerSocket is where the relayed server socket appears inside a
// container. It lives on the container's own filesystem, not on a bind mount:
// a unix socket created on the macOS host is not usable through a Docker
// Desktop bind mount ("operation not supported" on the container side), so the
// relay has to terminate inside the container.
const ContainerSocket = "/tmp/branchbox/jcode.sock"

// ContainerDebugSocket is the debug socket's container path. jcode derives it
// from the main socket's path by name, so the two must sit side by side.
const ContainerDebugSocket = "/tmp/branchbox/jcode-debug.sock"

// BridgePortFile records the host TCP port of the running relay, so repeated
// invocations reuse one relay instead of starting a second.
func BridgePortFile(home string) string {
	return filepath.Join(home, "branchbox", "bridge.port")
}

// Bridge is the pair of host ports relaying the server's sockets.
type Bridge struct {
	Port      int
	DebugPort int
}

// EnsureBridge makes sure TCP relays to the server's unix sockets are running
// on the host. TCP rather than a shared socket file because a bind-mounted
// unix socket does not work on macOS; the container side is re-exposed as a
// unix socket by ContainerRelayCommand.
func EnsureBridge(srv *Server, home, self string) (Bridge, error) {
	main, err := ensureOne(srv.Socket, BridgePortFile(home), self)
	if err != nil {
		return Bridge{}, err
	}
	b := Bridge{Port: main}
	if srv.DebugSocket != "" {
		// Best effort: the debug socket is optional, and jcode works without
		// it, so a failure here must not block the stack.
		if debug, err := ensureOne(srv.DebugSocket, debugPortFile(home), self); err == nil {
			b.DebugPort = debug
		}
	}
	return b, nil
}

func debugPortFile(home string) string {
	return filepath.Join(home, "branchbox", "bridge-debug.port")
}

func ensureOne(socket, portFile, self string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(portFile), 0o700); err != nil {
		return 0, fmt.Errorf("failed to create the bridge directory: %w", err)
	}
	if port, ok := readPort(portFile); ok && tcpAlive(port) {
		return port, nil
	}

	// Port 0 lets the kernel choose; the relay reports back through the file.
	cmd := exec.Command(self, "bridge", socket, portFile)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start the jcode bridge: %w", err)
	}
	// Detached: the relay must outlive the branchbox invocation that started
	// it, since containers use it for as long as they run.
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("failed to detach the jcode bridge: %w", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if port, ok := readPort(portFile); ok && tcpAlive(port) {
			return port, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("the jcode bridge did not come up")
}

// ContainerRelayCommand is the in-container half of the bridge: it re-exposes
// the host's TCP relays as unix sockets, which is what jcode connects to.
func ContainerRelayCommand(b Bridge) string {
	parts := []string{fmt.Sprintf("mkdir -p %s", filepath.Dir(ContainerSocket))}
	parts = append(parts, relayOne(ContainerSocket, b.Port))
	if b.DebugPort > 0 {
		parts = append(parts, relayOne(ContainerDebugSocket, b.DebugPort))
	}
	return strings.Join(parts, " && ")
}

func relayOne(socket string, port int) string {
	// setsid so the relay survives the exec that started it.
	return fmt.Sprintf("rm -f %s && (setsid socat UNIX-LISTEN:%s,fork,mode=600 TCP:host.docker.internal:%d >/dev/null 2>&1 &)",
		socket, socket, port)
}

func readPort(path string) (int, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

func tcpAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RunBridge accepts TCP connections on loopback and relays each to the jcode
// server's unix socket, writing the chosen port to portFile. branchbox
// re-execs itself in this mode rather than depending on socat, which is not
// installed on a stock macOS.
func RunBridge(target, portFile string) error {
	// Loopback only: this is a direct line to the user's jcode server, and
	// containers reach it through Docker's host gateway.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(portFile, []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		return fmt.Errorf("failed to record the bridge port: %w", err)
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
