package frontdoor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/miccah/social-security/internal/registry"
	"github.com/miccah/social-security/internal/vm"
)

// fakeVMTarget stands in for the VM manager.
type fakeVMTarget struct {
	target vm.Target
	err    error
}

func (f fakeVMTarget) Target() (vm.Target, error) { return f.target, f.err }

func genSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startFrontDoor(t *testing.T, reg registry.Registry, vmm vmTarget) string {
	t.Helper()
	addr := freeAddr(t)
	t.Setenv(addrEnv, addr)
	fd := New(reg, vmm)
	if err := fd.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { fd.Stop(context.Background()) })
	return addr
}

func dialGuest(t *testing.T, addr string) *gossh.Client {
	t.Helper()
	signer := genSigner(t)
	var lastErr error
	for i := 0; i < 100; i++ {
		c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
			User:            "guest",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			Timeout:         2 * time.Second,
		})
		if err == nil {
			return c
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial front door: %v", lastErr)
	return nil
}

// vmRecorder captures what the bridge asked the (fake) guest sshd to do.
type vmRecorder struct {
	mu      sync.Mutex
	command string
	ptyCols uint32
	ptyRows uint32
	resizes [][2]uint32
}

func (r *vmRecorder) setCommand(c string) { r.mu.Lock(); r.command = c; r.mu.Unlock() }
func (r *vmRecorder) setPty(c, rows uint32) {
	r.mu.Lock()
	r.ptyCols, r.ptyRows = c, rows
	r.mu.Unlock()
}
func (r *vmRecorder) addResize(c, rows uint32) {
	r.mu.Lock()
	r.resizes = append(r.resizes, [2]uint32{c, rows})
	r.mu.Unlock()
}
func (r *vmRecorder) cmd() string { r.mu.Lock(); defer r.mu.Unlock(); return r.command }
func (r *vmRecorder) pty() (uint32, uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ptyCols, r.ptyRows
}
func (r *vmRecorder) hasResize(c, rows uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.resizes {
		if w[0] == c && w[1] == rows {
			return true
		}
	}
	return false
}

// newFakeVM stands up an in-process sshd that accepts only authorized and, for
// each session, records the pty size, resizes, and exec command, then echoes the
// channel so bytes can be observed flowing through the bridge.
func newFakeVM(t *testing.T, authorized gossh.PublicKey) (string, *vmRecorder) {
	t.Helper()
	rec := &vmRecorder{}
	cfg := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return &gossh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	cfg.AddHostKey(genSigner(t))

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
			go serveFakeVM(c, cfg, rec)
		}
	}()
	return l.Addr().String(), rec
}

func serveFakeVM(nConn net.Conn, cfg *gossh.ServerConfig, rec *vmRecorder) {
	defer nConn.Close()
	conn, chans, reqs, err := gossh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go gossh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(gossh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func(ch gossh.Channel, reqs <-chan *gossh.Request) {
			for req := range reqs {
				switch req.Type {
				case "pty-req":
					var p struct {
						Term                          string
						Cols, Rows, WidthPx, HeightPx uint32
						Modes                         string
					}
					gossh.Unmarshal(req.Payload, &p)
					rec.setPty(p.Cols, p.Rows)
					req.Reply(true, nil)
				case "window-change":
					var w struct{ Cols, Rows, WidthPx, HeightPx uint32 }
					gossh.Unmarshal(req.Payload, &w)
					rec.addResize(w.Cols, w.Rows)
				case "exec":
					var e struct{ Command string }
					gossh.Unmarshal(req.Payload, &e)
					rec.setCommand(e.Command)
					req.Reply(true, nil)
					go io.Copy(ch, ch)
				case "shell":
					req.Reply(true, nil)
					go io.Copy(ch, ch)
				default:
					if req.WantReply {
						req.Reply(false, nil)
					}
				}
			}
		}(ch, chReqs)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func readContains(t *testing.T, r io.Reader, want string, timeout time.Duration) {
	t.Helper()
	found := make(chan struct{})
	go func() {
		var buf bytes.Buffer
		tmp := make([]byte, 128)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
				if strings.Contains(buf.String(), want) {
					close(found)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q in output", want)
	}
}

// A stock ssh client lands in the shared pairing session (never a host shell),
// its terminal size and resizes reach the VM, bytes flow both ways, and the
// registry tracks the connection and frees it on disconnect.
func TestFrontDoorBridgesGuestIntoPairing(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, rec := newFakeVM(t, sessionSigner.PublicKey())

	reg := registry.New()
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	frontAddr := startFrontDoor(t, reg, target)

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}

	// The bridge attaches to the pairing session, not a host shell.
	waitFor(t, 5*time.Second, func() bool { return rec.cmd() == "tmux new-session -A -s pairing" },
		"VM never received the pairing attach command")
	// The guest's terminal size is mirrored into the VM session.
	waitFor(t, 5*time.Second, func() bool { c, r := rec.pty(); return c == 80 && r == 24 },
		"VM never received the guest pty size")

	// A client resize propagates to the VM pane.
	if err := sess.WindowChange(40, 100); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return rec.hasResize(100, 40) },
		"resize did not propagate to the VM")

	// Bytes flow both ways: the VM echo comes back to the guest.
	if _, err := io.WriteString(stdin, "ping\n"); err != nil {
		t.Fatal(err)
	}
	readContains(t, stdout, "ping", 5*time.Second)

	if reg.Count() != 1 {
		t.Fatalf("active count = %d, want 1 while connected", reg.Count())
	}

	client.Close()
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 0 },
		"session was not removed from the registry after disconnect")
}

// When the sandbox is not ready the guest is told and disconnected, and no
// session lingers in the registry.
func TestFrontDoorSandboxNotReady(t *testing.T) {
	reg := registry.New()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{err: errors.New("not started")})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}

	readContains(t, stderr, "sandbox not ready", 5*time.Second)
	sess.Wait()
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 0 },
		"session was not removed after the sandbox-not-ready path")
}

// A failed bind aborts Start with an error rather than serving.
func TestFrontDoorStartBindError(t *testing.T) {
	t.Setenv(addrEnv, "127.0.0.1:99999") // out-of-range port
	fd := New(registry.New(), fakeVMTarget{})
	if err := fd.Start(context.Background()); err == nil {
		fd.Stop(context.Background())
		t.Fatal("Start should error on an unbindable address")
	}
}
