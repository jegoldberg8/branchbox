package jcode

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRunBridgeRelaysToTheServerSocket exercises the relay against a real unix
// socket, since the whole point of this code is that the transport behaves
// differently than a bind mount would.
func TestRunBridgeRelaysToTheServerSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "server.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// An echo server standing in for the jcode daemon.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	portFile := filepath.Join(dir, "bridge.port")
	go func() { _ = RunBridge(sock, portFile) }()

	port := waitForPort(t, portFile)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		t.Fatalf("the bridge is not accepting connections: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("no response through the bridge: %v", err)
	}
	if string(buf) != "hello\n" {
		t.Errorf("relayed %q, want the echoed payload", buf)
	}
}

func TestRunBridgeBindsLoopbackOnly(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "server.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	portFile := filepath.Join(dir, "bridge.port")
	go func() { _ = RunBridge(sock, portFile) }()
	port := waitForPort(t, portFile)

	// The bridge is a direct line to the user's jcode server, credentials
	// included, so it must never be reachable from off the machine.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ipnet.IP.String(), strconv.Itoa(port)), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Errorf("the bridge is reachable on %s; it must bind loopback only", ipnet.IP)
		}
	}
}

func TestContainerRelayCommandCoversBothSockets(t *testing.T) {
	cmd := ContainerRelayCommand(Bridge{Port: 5000, DebugPort: 5001})
	for _, want := range []string{ContainerSocket, ContainerDebugSocket, "5000", "5001", "host.docker.internal"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("relay command is missing %q: %s", want, cmd)
		}
	}
	// jcode derives the debug socket's path from the main one, so they have to
	// be siblings.
	if filepath.Dir(ContainerSocket) != filepath.Dir(ContainerDebugSocket) {
		t.Error("the sockets must live in the same directory")
	}

	// Without a debug socket the command must not reference one.
	cmd = ContainerRelayCommand(Bridge{Port: 5000})
	if strings.Contains(cmd, ContainerDebugSocket) {
		t.Errorf("no debug port was given, yet the command relays one: %s", cmd)
	}
}

func TestRunningIgnoresRegistryEntriesWithoutALiveSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JCODE_HOME", home)

	// Unix socket paths have a ~104 byte limit, which a nested TempDir path
	// can exceed; bind the listener somewhere short.
	sockDir, err := os.MkdirTemp("/tmp", "bb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "live.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// A stale entry is the common case: the registry file outlives the
	// process that wrote it.
	registry := map[string]Server{
		"stale": {Name: "stale", Socket: filepath.Join(home, "gone.sock")},
		"live":  {Name: "live", Socket: sock},
	}
	body, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "servers.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Running()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "live" {
		t.Fatalf("Running() = %+v, want the server whose socket exists", got)
	}
}

func TestRunningReportsNoServerWhenTheRegistryIsAbsent(t *testing.T) {
	t.Setenv("JCODE_HOME", t.TempDir())
	got, err := Running()
	if err != nil {
		t.Fatalf("a missing registry is not an error: %v", err)
	}
	if got != nil {
		t.Errorf("Running() = %+v, want nil", got)
	}
}

func waitForPort(t *testing.T, portFile string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if port, ok := readPort(portFile); ok {
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the bridge never reported a port")
	return 0
}
