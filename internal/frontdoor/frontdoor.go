// Package frontdoor is the SSH front door: a wish server on a loopback/LAN
// address that runs the join ceremony (username, SSN, and owner approval) and
// bridges accepted connections into the VM's shared tmux session. Guests only
// ever touch this front door; they never reach a host shell.
package frontdoor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/miccah/social-security/internal/bridge"
	"github.com/miccah/social-security/internal/lifecycle"
	"github.com/miccah/social-security/internal/registry"
	"github.com/miccah/social-security/internal/vm"
)

// addrEnv overrides the front door listen address.
const addrEnv = "SSSH_FRONTDOOR_ADDR"

// defaultAddr is the listen address when addrEnv is unset. Binding to all
// interfaces lets other LAN clients reach the shared session.
const defaultAddr = ":1337"

// hostKeyEnv overrides the path to the front door's persistent SSH host key.
const hostKeyEnv = "SSSH_HOST_KEY"

// pendingTimeout bounds how long a guest waits for the owner's decision before
// the request is dropped and the guest disconnected.
var pendingTimeout = 2 * time.Minute

// Manager runs the SSH front door and bridges accepted sessions.
type Manager interface {
	lifecycle.Manager

	// Addr returns the bound listen address. It is valid after Start.
	Addr() string
}

// vmTarget is the slice of the VM manager the front door needs: how to reach the
// guest sshd. An interface so the front door depends on behavior, not the
// concrete manager.
type vmTarget interface {
	Target() (vm.Target, error)
}

type manager struct {
	reg        registry.Registry
	vm         vmTarget
	endSession func() // called when the owner disconnects, to end the whole session

	srv *ssh.Server
	ln  net.Listener
}

// New returns a front door that bridges LAN sessions into the VM's shared tmux,
// tracking each connection in the registry. endSession is called when the owner
// disconnects, to tear down the session for everyone.
func New(reg registry.Registry, vmm vmTarget, endSession func()) Manager {
	return &manager{reg: reg, vm: vmm, endSession: endSession}
}

func (m *manager) Name() string { return "frontdoor" }

// Addr returns the bound listen address, or "" before Start has bound one.
func (m *manager) Addr() string {
	if m.ln == nil {
		return ""
	}
	return m.ln.Addr().String()
}

// Start binds the listener synchronously (so a bind failure aborts startup) and
// serves on a goroutine (so Start does not block for the server's lifetime).
func (m *manager) Start(context.Context) error {
	addr := os.Getenv(addrEnv)
	if addr == "" {
		addr = defaultAddr
	}

	keyPath, err := hostKeyPath()
	if err != nil {
		return err
	}

	m.srv, err = wish.NewServer(
		// A persistent host key gives clients a constant identity across runs, so
		// the owner's known_hosts entry does not keep mismatching.
		wish.WithHostKeyPath(keyPath),
		// Accept any client: the front door does not authenticate, it bridges
		// every connection straight into the shared session.
		wish.WithPublicKeyAuth(func(ssh.Context, ssh.PublicKey) bool { return true }),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return true }),
		wish.WithMiddleware(m.joinMiddleware),
	)
	if err != nil {
		m.teardown()
		return fmt.Errorf("build ssh server: %w", err)
	}

	m.ln, err = net.Listen("tcp", addr)
	if err != nil {
		m.teardown()
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	slog.Info("frontdoor: listening", "addr", m.ln.Addr().String())

	go func() {
		if err := m.srv.Serve(m.ln); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			slog.Error("frontdoor: serve failed", "err", err)
		}
	}()
	return nil
}

func (m *manager) Stop(context.Context) error {
	m.teardown()
	return nil
}

func (m *manager) teardown() {
	if m.srv != nil {
		m.srv.Close() // also closes m.ln
		m.srv = nil
	}
	m.ln = nil
}

// hostKeyPath returns the path to the front door's SSH host key. Reusing one key
// across runs gives clients a constant host identity, so they do not hit
// known_hosts mismatches. It honors hostKeyEnv, otherwise the key lives under the
// user config dir. The parent directory is created; wish generates the key on
// first use and loads it thereafter.
func hostKeyPath() (string, error) {
	path := os.Getenv(hostKeyEnv)
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
		path = filepath.Join(dir, "sssh", "host_ed25519")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create host key dir: %w", err)
	}
	return path, nil
}

// joinMiddleware handles each connection: the first claims the owner slot and is
// bridged straight into the shared tmux; every later connection is a guest that
// runs the join ceremony. It ignores next because the front door is the terminal
// handler, not a link in a chain.
func (m *manager) joinMiddleware(ssh.Handler) ssh.Handler {
	return func(s ssh.Session) {
		if _, _, ok := s.Pty(); !ok {
			fmt.Fprint(s.Stderr(), "sssh: a terminal is required (connect with: ssh -t)\r\n")
			s.Exit(1)
			return
		}
		if m.reg.ClaimOwner() {
			m.serveOwner(s)
			return
		}
		m.serveGuest(s)
	}
}

// serveOwner bridges the owner straight into the shared tmux, with no ceremony
// and no input interception. When this method returns, all sessions will be
// closed.
func (m *manager) serveOwner(s ssh.Session) {
	// The owner leaving ends the session for everyone.
	defer m.endSession()
	defer m.reg.ReleaseOwner()
	remote := s.RemoteAddr().String()
	slog.Info("frontdoor: owner connected", "remote", remote)
	defer slog.Info("frontdoor: owner disconnected", "remote", remote)

	target, err := m.vm.Target()
	if err != nil {
		slog.Error("frontdoor: sandbox not ready", "err", err)
		fmt.Fprint(s, "sssh: sandbox not ready\r\n")
		s.Exit(1)
		return
	}
	if err := bridge.New(target, s, s).Run(s.Context()); err != nil {
		slog.Error("frontdoor: owner bridge ended", "err", err)
		s.Exit(1)
	}
}

// serveGuest runs the join ceremony, waits for the owner's decision, and bridges
// an accepted guest into the shared tmux.
func (m *manager) serveGuest(s ssh.Session) {
	remote := s.RemoteAddr().String()
	t := term.NewTerminal(s, "")
	req, err := m.join(s, t)
	if err != nil {
		// The guest disconnected before submitting a request; nothing to clean.
		slog.Info("frontdoor: join abandoned", "remote", remote, "err", err)
		return
	}
	slog.Info("frontdoor: join requested", "id", req.ID, "user", req.Username, "remote", remote,
		"pending", len(m.reg.Pending()))

	// Pump guest input so a Ctrl+C can cancel the request while queued; once
	// accepted, the same stream feeds the bridge (one reader of the session).
	pr, pw := io.Pipe()
	var waiting atomic.Bool
	waiting.Store(true)
	interrupt := make(chan struct{}, 1)
	go pumpInput(s, pw, &waiting, interrupt)

	fmt.Fprint(s, "waiting for the owner to approve… (ctrl-c to cancel)\r\n")
	select {
	case d := <-req.Decision():
		if d == registry.Decline {
			fmt.Fprint(s, "request declined\r\n")
			s.Exit(1)
			return
		}
	case <-interrupt:
		m.reg.CancelPending(req.ID)
		fmt.Fprint(s, "cancelled\r\n")
		s.Exit(130) // 128 + SIGINT
		return
	case <-time.After(pendingTimeout):
		m.reg.CancelPending(req.ID)
		fmt.Fprint(s, "request timed out\r\n")
		s.Exit(1)
		return
	case <-s.Context().Done():
		m.reg.CancelPending(req.ID)
		return
	}
	waiting.Store(false) // Ctrl+C now passes through to the shared session

	sess := m.reg.Activate(req, func() { s.Close() })
	slog.Info("frontdoor: guest accepted", "id", sess.ID, "user", sess.Username, "active", m.reg.Count())
	defer func() {
		m.reg.RemoveActive(sess.ID)
		slog.Info("frontdoor: guest disconnected", "id", sess.ID, "user", sess.Username, "active", m.reg.Count())
	}()

	target, err := m.vm.Target()
	if err != nil {
		slog.Error("frontdoor: sandbox not ready", "err", err)
		fmt.Fprint(s, "sssh: sandbox not ready\r\n")
		s.Exit(1)
		return
	}
	if err := bridge.New(target, s, pr).Run(s.Context()); err != nil {
		slog.Error("frontdoor: bridge ended", "id", sess.ID, "err", err)
		s.Exit(1)
		return
	}
}

// ctrlC is the byte a terminal sends for Ctrl+C.
const ctrlC = 0x03

// pumpInput copies guest input to w. While waiting is set, type-ahead is
// discarded and a Ctrl+C signals interrupt and stops the pump; once waiting is
// cleared, every byte (Ctrl+C included) is forwarded so the shared session sees
// it. It returns when the guest input closes.
func pumpInput(r io.Reader, w *io.PipeWriter, waiting *atomic.Bool, interrupt chan<- struct{}) {
	defer w.Close()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if waiting.Load() {
				if bytes.IndexByte(chunk, ctrlC) >= 0 {
					select {
					case interrupt <- struct{}{}:
					default:
					}
					return
				}
			} else if _, werr := w.Write(chunk); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// join prompts for a username (defaulting to the SSH login username) and an SSN,
// reprompting until the guest picks a name that is free, and registers the
// pending request. The SSN is an out-of-band matching token, not a secret, so it
// is echoed like any other input.
func (m *manager) join(s ssh.Session, t *term.Terminal) (*registry.Request, error) {
	remote := s.RemoteAddr().String()
	defaultName := s.User()
	for {
		if defaultName != "" {
			t.SetPrompt(fmt.Sprintf("username [%s]: ", defaultName))
		} else {
			t.SetPrompt("username: ")
		}
		username, err := t.ReadLine()
		if err != nil {
			return nil, err
		}
		username = strings.TrimSpace(username)
		if username == "" {
			username = defaultName
		}
		if username == "" {
			continue
		}
		t.SetPrompt("SSN: ")
		ssn, err := t.ReadLine()
		if err != nil {
			return nil, err
		}
		req, err := m.reg.AddPending(username, strings.TrimSpace(ssn), remote)
		if errors.Is(err, registry.ErrUsernameTaken) {
			fmt.Fprintf(s, "username %q is taken, choose another\r\n", username)
			continue
		}
		if err != nil {
			return nil, err
		}
		return req, nil
	}
}
