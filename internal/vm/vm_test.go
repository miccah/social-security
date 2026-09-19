package vm

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// Target is only valid after Start; a fresh manager reports it is not ready.
func TestTargetBeforeStart(t *testing.T) {
	if _, err := New("/tmp/project").Target(); err == nil {
		t.Fatal("Target() should error before Start")
	}
}

// freeLoopbackPort returns a real, currently free loopback port.
func TestFreeLoopbackPort(t *testing.T) {
	p, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 {
		t.Fatalf("port = %d, want > 0", p)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("port %d not reusable: %v", p, err)
	}
	l.Close()
}

// bannerServer accepts loopback connections and writes banner to each, returning
// the chosen port.
func bannerServer(t *testing.T, banner string) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(banner))
			c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// sshBanner recognizes an SSH greeting and rejects anything else.
func TestSSHBanner(t *testing.T) {
	sshd := bannerServer(t, "SSH-2.0-Test\r\n")
	if !sshBanner(fmt.Sprintf("127.0.0.1:%d", sshd)) {
		t.Error("sshBanner should be true for an SSH banner")
	}
	other := bannerServer(t, "220 not ssh\r\n")
	if sshBanner(fmt.Sprintf("127.0.0.1:%d", other)) {
		t.Error("sshBanner should be false for a non-SSH banner")
	}
	if sshBanner("127.0.0.1:1") {
		t.Error("sshBanner should be false when nothing is listening")
	}
}

// waitForSSH returns once the guest greets with an SSH banner.
func TestWaitForSSHReady(t *testing.T) {
	port := bannerServer(t, "SSH-2.0-Test\r\n")
	m := &manager{port: port, exited: make(chan struct{})}
	if err := m.waitForSSH(context.Background()); err != nil {
		t.Fatalf("waitForSSH: %v", err)
	}
}

// waitForSSH fails fast, without waiting out the boot timeout, when QEMU exits
// before sshd is ready.
func TestWaitForSSHQEMUExited(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	m := &manager{port: 1, exited: exited, runtimeDir: t.TempDir()}
	if err := m.waitForSSH(context.Background()); err == nil {
		t.Fatal("waitForSSH should error when QEMU has exited")
	}
}

// waitForSSH returns a clear error when the deadline elapses with no banner.
func TestWaitForSSHTimeout(t *testing.T) {
	m := &manager{port: 1, exited: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := m.waitForSSH(ctx); err == nil {
		t.Fatal("waitForSSH should error when the deadline elapses")
	}
}
