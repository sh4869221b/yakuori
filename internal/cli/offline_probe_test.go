//go:build offlineprobe

package cli

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOfflineNetworkProbe performs exactly one network or resolver-directed attempt
// selected by YAKUORI_NETWORK_PROBE and swallows every failure: the attempt itself
// is the observable, so a refused connection still exits 0. The variable is read
// only here, in a test-only file, so no product surface or environment grows.
func TestOfflineNetworkProbe(t *testing.T) {
	switch mode := os.Getenv("YAKUORI_NETWORK_PROBE"); mode {
	case "tcp4":
		dialTCP("tcp4", "127.0.0.1:9")
	case "tcp6":
		dialTCP("tcp6", "[::1]:9")
	case "udp4":
		sendUDP("udp4", "127.0.0.1:9")
	case "udp6":
		sendUDP("udp6", "[::1]:9")
	case "unix":
		dialDialer("unix", "/run/systemd/resolve/io.systemd.Resolve")
	case "unixgram":
		sendUNIXGram("/run/systemd/resolve/io.systemd.Resolve")
	case "thread":
		done := make(chan struct{})
		go func() {
			defer close(done)
			dialTCP("tcp4", "127.0.0.1:9")
		}()
		<-done
	case "child":
		child := exec.Command(os.Args[0], "-test.run=^TestOfflineNetworkProbe$")
		child.Env = append(os.Environ(), "YAKUORI_NETWORK_PROBE=tcp4")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		_ = child.Run()
	default:
		t.Fatalf("YAKUORI_NETWORK_PROBE=%q is not a known probe mode", mode)
	}
}

func dialTCP(network, address string) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return
	}
	_ = conn.Close()
}

func dialDialer(network, address string) {
	dialTCP(network, address)
}

func sendUDP(network, address string) {
	target, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		return
	}
	conn, err := net.ListenPacket(network, "")
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.WriteTo([]byte("probe"), target)
}

func sendUNIXGram(destination string) {
	local, err := os.MkdirTemp("", "yakuori-offline-probe-")
	if err != nil {
		return
	}
	defer func() { _ = os.RemoveAll(local) }()
	conn, err := net.ListenPacket("unixgram", filepath.Join(local, "probe.sock"))
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.WriteTo([]byte("probe"), &net.UnixAddr{Name: destination, Net: "unixgram"})
}
