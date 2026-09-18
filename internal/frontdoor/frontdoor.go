// Package frontdoor is the SSH front door: a wish server on a loopback/LAN
// address that bridges each accepted connection into the VM's shared tmux
// session. Guests only ever touch this front door; they never reach a host
// shell.
package frontdoor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"

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

// Manager runs the SSH front door and bridges accepted sessions.
type Manager interface {
	lifecycle.Manager
}

// vmTarget is the slice of the VM manager the front door needs: how to reach the
// guest sshd. An interface so the front door depends on behavior, not the
// concrete manager.
type vmTarget interface {
	Target() (vm.Target, error)
}

type manager struct {
	reg registry.Registry
	vm  vmTarget

	runtimeDir string
	srv        *ssh.Server
	ln         net.Listener
}

// New returns a front door that bridges LAN sessions into the VM's shared tmux,
// tracking each connection in the registry.
func New(reg registry.Registry, vmm vmTarget) Manager {
	return &manager{reg: reg, vm: vmm}
}

func (m *manager) Name() string { return "frontdoor" }

// Start binds the listener synchronously (so a bind failure aborts startup) and
// serves on a goroutine (so Start does not block for the server's lifetime).
func (m *manager) Start(context.Context) error {
	addr := os.Getenv(addrEnv)
	if addr == "" {
		addr = defaultAddr
	}

	var err error
	m.runtimeDir, err = os.MkdirTemp("", "sssh-frontdoor-")
	if err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}

	m.srv, err = wish.NewServer(
		// A host key persisted for the session's lifetime avoids host-key churn
		// across repeat LAN connects.
		wish.WithHostKeyPath(filepath.Join(m.runtimeDir, "host_ed25519")),
		// Accept any client: the front door does not authenticate, it bridges
		// every connection straight into the shared session.
		wish.WithPublicKeyAuth(func(ssh.Context, ssh.PublicKey) bool { return true }),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return true }),
		wish.WithMiddleware(m.bridgeMiddleware),
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
	if m.runtimeDir != "" {
		os.RemoveAll(m.runtimeDir)
		m.runtimeDir = ""
	}
}

// bridgeMiddleware handles each accepted session: register it, resolve the VM
// target, and bridge into the shared tmux. It ignores next because the front
// door is the terminal handler, not a link in a chain.
func (m *manager) bridgeMiddleware(ssh.Handler) ssh.Handler {
	return func(s ssh.Session) {
		remote := s.RemoteAddr().String()
		// Every connection is treated as owner-side; origin-based guest
		// classification is not done here.
		sess := m.reg.Add(remote, true)
		slog.Info("frontdoor: session connected", "id", sess.ID, "remote", remote, "active", m.reg.Count())
		defer func() {
			m.reg.Remove(sess.ID)
			slog.Info("frontdoor: session disconnected", "id", sess.ID, "remote", remote, "active", m.reg.Count())
		}()

		if _, _, ok := s.Pty(); !ok {
			fmt.Fprintln(s.Stderr(), "sssh: a terminal is required (connect with: ssh -t)")
			s.Exit(1)
			return
		}

		target, err := m.vm.Target()
		if err != nil {
			slog.Error("frontdoor: sandbox not ready", "err", err)
			fmt.Fprintln(s.Stderr(), "sssh: sandbox not ready")
			s.Exit(1)
			return
		}

		if err := bridge.New(target, s).Run(s.Context()); err != nil {
			slog.Error("frontdoor: bridge ended", "id", sess.ID, "err", err)
			s.Exit(1)
			return
		}
	}
}
