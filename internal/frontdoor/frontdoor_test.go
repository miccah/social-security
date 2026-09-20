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
	"os"
	"path/filepath"
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

// guestRegistry returns a registry with the owner slot already claimed, so a
// connecting client is treated as a guest rather than the owner.
func guestRegistry() registry.Registry {
	reg := registry.New()
	reg.ClaimOwner()
	return reg
}

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
	return startFrontDoorQuit(t, reg, vmm, func() {})
}

func startFrontDoorQuit(t *testing.T, reg registry.Registry, vmm vmTarget, quit func()) string {
	t.Helper()
	addr := freeAddr(t)
	t.Setenv(addrEnv, addr)
	t.Setenv(hostKeyEnv, filepath.Join(t.TempDir(), "host_ed25519"))
	fd := New(reg, vmm, nil, quit)
	if err := fd.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { fd.Stop(context.Background()) })
	return addr
}

// fakeIngress exposes a plain listener as the public tunnel ingress.
type fakeIngress struct{ ln net.Listener }

func (f fakeIngress) Listener() net.Listener { return f.ln }

// startFrontDoorGuest starts a front door with a public guest listener alongside
// the LAN one and returns the guest listener's address.
func startFrontDoorGuest(t *testing.T, reg registry.Registry, vmm vmTarget) string {
	t.Helper()
	t.Setenv(addrEnv, freeAddr(t))
	t.Setenv(hostKeyEnv, filepath.Join(t.TempDir(), "host_ed25519"))
	gl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fd := New(reg, vmm, fakeIngress{gl}, func() {})
	if err := fd.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		fd.Stop(context.Background())
		gl.Close()
	})
	return gl.Addr().String()
}

func dialGuest(t *testing.T, addr string) *gossh.Client { return dialGuestAs(t, addr, "guest") }

func dialGuestAs(t *testing.T, addr, user string) *gossh.Client {
	t.Helper()
	signer := genSigner(t)
	var lastErr error
	for i := 0; i < 100; i++ {
		c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
			User:            user,
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

// openShell opens a pty session and starts a shell, returning the pipes the join
// ceremony reads and writes.
func openShell(t *testing.T, client *gossh.Client) (*gossh.Session, io.WriteCloser, io.Reader) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
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
	return sess, stdin, stdout
}

// writeLine sends one ceremony answer; the terminal treats CR as Enter.
func writeLine(t *testing.T, w io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(w, line+"\r"); err != nil {
		t.Fatal(err)
	}
}

// awaitPending blocks until a request for username is queued, then returns it.
func awaitPending(t *testing.T, reg registry.Registry, username string) registry.Request {
	t.Helper()
	var got registry.Request
	waitFor(t, 5*time.Second, func() bool {
		for _, p := range reg.Pending() {
			if p.Username == username {
				got = p
				return true
			}
		}
		return false
	}, "request for "+username+" never landed in pending")
	return got
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

// A guest completes the ceremony, waits, is accepted by the owner, and is then
// bridged into the shared pairing session: the tmux attach runs on the VM, the
// terminal size and resizes reach it, bytes flow both ways, and the registry
// tracks the connection and frees it on disconnect.
func TestGuestJoinAcceptBridges(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, rec := newFakeVM(t, sessionSigner.PublicKey())

	reg := guestRegistry()
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	frontAddr := startFrontDoor(t, reg, target)

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "alice")
	writeLine(t, stdin, "123-45-6789")

	req := awaitPending(t, reg, "alice")
	if req.SSN != "123-45-6789" {
		t.Fatalf("SSN = %q, want 123-45-6789", req.SSN)
	}
	if err := reg.Resolve(req.ID, registry.Accept); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 5*time.Second, func() bool { return rec.cmd() == "tmux new-session -A -s pairing" },
		"guest was not bridged into the pairing session")
	waitFor(t, 5*time.Second, func() bool { c, r := rec.pty(); return c == 80 && r == 24 },
		"guest pty size not mirrored to the VM")

	if err := sess.WindowChange(40, 100); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return rec.hasResize(100, 40) },
		"resize did not propagate to the VM")

	io.WriteString(stdin, "ping\n")
	readContains(t, stdout, "ping", 5*time.Second)

	if reg.Count() != 1 {
		t.Fatalf("active count = %d, want 1 while connected", reg.Count())
	}
	client.Close()
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 0 },
		"session not removed from the registry after disconnect")
}

// An empty username defaults to the SSH login username.
func TestGuestUsernameDefaultsToSSHUser(t *testing.T) {
	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuestAs(t, frontAddr, "frank")
	defer client.Close()
	_, stdin, _ := openShell(t, client)

	writeLine(t, stdin, "") // accept the default
	writeLine(t, stdin, "ssn")
	awaitPending(t, reg, "frank")
}

// A taken username is rejected and the guest is reprompted until one is free.
func TestGuestRepromptedOnTakenUsername(t *testing.T) {
	reg := guestRegistry()
	if _, err := reg.AddPending("alice", "x", "10.0.0.9:1"); err != nil {
		t.Fatal(err)
	}
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	_, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "alice") // taken
	writeLine(t, stdin, "ssn")
	writeLine(t, stdin, "alice2") // free
	writeLine(t, stdin, "ssn")

	readContains(t, stdout, "taken", 5*time.Second)
	awaitPending(t, reg, "alice2")
}

// Declining tells the guest and frees the username.
func TestGuestDeclined(t *testing.T) {
	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "bob")
	writeLine(t, stdin, "ssn")
	req := awaitPending(t, reg, "bob")
	if err := reg.Resolve(req.ID, registry.Decline); err != nil {
		t.Fatal(err)
	}

	readContains(t, stdout, "declined", 5*time.Second)
	sess.Wait()
	if _, err := reg.AddPending("bob", "x", "y"); err != nil {
		t.Fatalf("username should be free after decline: %v", err)
	}
}

// A pending request that no one resolves times out, tells the guest, and frees
// the username.
func TestPendingTimeout(t *testing.T) {
	prev := pendingTimeout
	pendingTimeout = 200 * time.Millisecond
	defer func() { pendingTimeout = prev }()

	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	_, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "carol")
	writeLine(t, stdin, "ssn")

	readContains(t, stdout, "timed out", 5*time.Second)
	waitFor(t, 5*time.Second, func() bool { return len(reg.Pending()) == 0 }, "pending not cleared after timeout")
	if _, err := reg.AddPending("carol", "x", "y"); err != nil {
		t.Fatalf("username should be free after timeout: %v", err)
	}
}

// A guest that disconnects while pending drops its request and frees the name.
func TestGuestDisconnectWhilePending(t *testing.T) {
	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuest(t, frontAddr)
	_, stdin, _ := openShell(t, client)
	writeLine(t, stdin, "dave")
	writeLine(t, stdin, "ssn")
	awaitPending(t, reg, "dave")

	client.Close()
	waitFor(t, 5*time.Second, func() bool { return len(reg.Pending()) == 0 }, "pending not cleared after disconnect")
	if _, err := reg.AddPending("dave", "x", "y"); err != nil {
		t.Fatalf("username should be free after disconnect: %v", err)
	}
}

// A guest can Ctrl+C out of the queue, which drops the request and frees the name.
func TestGuestCancelsWithCtrlC(t *testing.T) {
	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "grace")
	writeLine(t, stdin, "ssn")
	awaitPending(t, reg, "grace")

	if _, err := stdin.Write([]byte{0x03}); err != nil { // Ctrl+C
		t.Fatal(err)
	}
	readContains(t, stdout, "cancelled", 5*time.Second)
	sess.Wait()

	waitFor(t, 5*time.Second, func() bool { return len(reg.Pending()) == 0 }, "pending not cleared after ctrl-c")
	if _, err := reg.AddPending("grace", "x", "y"); err != nil {
		t.Fatalf("username should be free after ctrl-c: %v", err)
	}
}

// An accepted guest whose sandbox is unreachable is told and disconnected, with
// no session left behind.
func TestSandboxNotReady(t *testing.T) {
	reg := guestRegistry()
	frontAddr := startFrontDoor(t, reg, fakeVMTarget{err: errors.New("not started")})

	client := dialGuest(t, frontAddr)
	defer client.Close()
	sess, stdin, stdout := openShell(t, client)

	writeLine(t, stdin, "erin")
	writeLine(t, stdin, "ssn")
	req := awaitPending(t, reg, "erin")
	if err := reg.Resolve(req.ID, registry.Accept); err != nil {
		t.Fatal(err)
	}

	readContains(t, stdout, "sandbox not ready", 5*time.Second)
	sess.Wait()
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 0 }, "active session not cleared")
}

// The first connection claims the owner slot and is bridged straight in with no
// ceremony; a later connection is a guest that must queue.
func TestFirstConnectionIsOwner(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, rec := newFakeVM(t, sessionSigner.PublicKey())

	reg := registry.New()
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	frontAddr := startFrontDoor(t, reg, target)

	owner := dialGuest(t, frontAddr)
	defer owner.Close()
	openShell(t, owner) // no prompts: the owner is bridged directly

	waitFor(t, 5*time.Second, func() bool { return rec.cmd() == "tmux new-session -A -s pairing" },
		"owner was not bridged straight into the pairing session")
	waitFor(t, 5*time.Second, reg.OwnerPresent, "owner slot not claimed")

	// A second connection is a guest and must run the ceremony.
	guest := dialGuest(t, frontAddr)
	defer guest.Close()
	_, stdin, _ := openShell(t, guest)
	writeLine(t, stdin, "bob")
	writeLine(t, stdin, "ssn")
	awaitPending(t, reg, "bob")
}

// A connection arriving on the public tunnel is always a guest and runs the join
// ceremony, even as the first connection with the owner slot free.
func TestTunnelConnectionIsAlwaysGuest(t *testing.T) {
	reg := registry.New()
	guestAddr := startFrontDoorGuest(t, reg, fakeVMTarget{})

	guest := dialGuest(t, guestAddr)
	defer guest.Close()
	_, stdin, _ := openShell(t, guest)
	writeLine(t, stdin, "bob")
	writeLine(t, stdin, "ssn")
	awaitPending(t, reg, "bob")

	if reg.OwnerPresent() {
		t.Fatal("a tunnel connection must not claim the owner slot")
	}
}

// The owner slot is freed on disconnect, so a later connection can take over.
func TestOwnerSlotReleasedOnDisconnect(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, _ := newFakeVM(t, sessionSigner.PublicKey())

	reg := registry.New()
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	frontAddr := startFrontDoor(t, reg, target)

	owner := dialGuest(t, frontAddr)
	openShell(t, owner)
	waitFor(t, 5*time.Second, reg.OwnerPresent, "owner slot not claimed")

	owner.Close()
	waitFor(t, 5*time.Second, func() bool { return !reg.OwnerPresent() },
		"owner slot not released on disconnect")
}

// The owner disconnecting ends the session for everyone.
func TestOwnerDisconnectEndsSession(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, _ := newFakeVM(t, sessionSigner.PublicKey())

	reg := registry.New()
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	ended := make(chan struct{}, 1)
	frontAddr := startFrontDoorQuit(t, reg, target, func() { ended <- struct{}{} })

	owner := dialGuest(t, frontAddr)
	openShell(t, owner)
	waitFor(t, 5*time.Second, reg.OwnerPresent, "owner slot not claimed")

	owner.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("owner disconnect should end the session")
	}
}

// A guest disconnecting frees its slot and username but leaves the session up.
func TestGuestDisconnectKeepsSession(t *testing.T) {
	sessionSigner := genSigner(t)
	vmAddr, rec := newFakeVM(t, sessionSigner.PublicKey())

	reg := registry.New()
	reg.ClaimOwner() // an owner is present
	target := fakeVMTarget{target: vm.Target{Addr: vmAddr, User: "root", Signer: sessionSigner}}
	ended := make(chan struct{}, 1)
	frontAddr := startFrontDoorQuit(t, reg, target, func() { ended <- struct{}{} })

	guest := dialGuest(t, frontAddr)
	_, stdin, _ := openShell(t, guest)
	writeLine(t, stdin, "bob")
	writeLine(t, stdin, "ssn")
	req := awaitPending(t, reg, "bob")
	if err := reg.Resolve(req.ID, registry.Accept); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return rec.cmd() == "tmux new-session -A -s pairing" }, "guest not bridged")
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 1 }, "guest not active")

	guest.Close()
	waitFor(t, 5*time.Second, func() bool { return reg.Count() == 0 }, "guest not removed")
	if _, err := reg.AddPending("bob", "x", "y"); err != nil {
		t.Fatalf("username should be free after a guest drop: %v", err)
	}
	select {
	case <-ended:
		t.Fatal("a guest disconnect must not end the session")
	default:
	}
}

// A failed bind aborts Start with an error rather than serving.
func TestStartBindError(t *testing.T) {
	t.Setenv(addrEnv, "127.0.0.1:99999") // out-of-range port
	t.Setenv(hostKeyEnv, filepath.Join(t.TempDir(), "host_ed25519"))
	fd := New(registry.New(), fakeVMTarget{}, nil, func() {})
	if err := fd.Start(context.Background()); err == nil {
		fd.Stop(context.Background())
		t.Fatal("Start should error on an unbindable address")
	}
}

// hostKeyPath honors the override and creates the parent directory.
func TestHostKeyPathHonorsEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "host_ed25519")
	t.Setenv(hostKeyEnv, path)
	got, err := hostKeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("hostKeyPath = %q, want %q", got, path)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

// The front door presents the same host key across restarts, so clients see a
// constant identity.
func TestHostKeyPersistsAcrossRestarts(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "host_ed25519")
	first := frontDoorHostKey(t, keyPath)
	second := frontDoorHostKey(t, keyPath)
	if first != second {
		t.Fatalf("host key changed across restarts: %s vs %s", first, second)
	}
}

// frontDoorHostKey starts a front door backed by keyPath, connects, and returns
// the server's host key fingerprint.
func frontDoorHostKey(t *testing.T, keyPath string) string {
	t.Helper()
	addr := freeAddr(t)
	t.Setenv(addrEnv, addr)
	t.Setenv(hostKeyEnv, keyPath)
	fd := New(registry.New(), fakeVMTarget{}, nil, func() {})
	if err := fd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fd.Stop(context.Background())

	signer := genSigner(t)
	var fp string
	var lastErr error
	for i := 0; i < 100; i++ {
		c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
			User: "probe",
			Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
				fp = gossh.FingerprintSHA256(key)
				return nil
			},
			Timeout: 2 * time.Second,
		})
		if err == nil {
			c.Close()
			return fp
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial front door: %v", lastErr)
	return ""
}
