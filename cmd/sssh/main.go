// Command sssh is the social-security entrypoint: it resolves the project
// directory, wires the managed subsystems, and owns the top-level context and
// graceful teardown.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/miccah/social-security/internal/coauthor"
	"github.com/miccah/social-security/internal/control"
	"github.com/miccah/social-security/internal/frontdoor"
	"github.com/miccah/social-security/internal/lifecycle"
	"github.com/miccah/social-security/internal/registry"
	"github.com/miccah/social-security/internal/tunnel"
	"github.com/miccah/social-security/internal/vm"
)

// shutdownTimeout bounds graceful teardown so a wedged subsystem can't hang the
// process forever. Teardown runs under its own deadline because the root
// context is already cancelled by the time we tear down.
const shutdownTimeout = 15 * time.Second

func main() {
	// When invoked as a git hook inside the sandbox VM (`sssh commit ...`), run
	// the commit helper and exit instead of starting the server.
	if len(os.Args) > 1 && os.Args[1] == "commit" {
		os.Exit(commitMain(os.Args[2:]))
	}
	if err := run(); err != nil {
		slog.Error("sssh exited with error", "err", err)
		os.Exit(1)
	}
}

// run resolves the project directory, wires the managed subsystems, starts them
// in dependency order, and blocks until the root context is cancelled before
// tearing everything down. A failed start unwinds whatever came up and aborts.
func run() error {
	// Cancel the root context on SIGINT/SIGTERM so a Ctrl-C in the launching
	// terminal drives graceful teardown instead of an abrupt exit. Everything
	// derives from this context, so one signal unwinds the whole session.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A cancelable child so the control plane can end the session when the owner
	// quits the launching terminal, the same way a signal does.
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	// The project is the current working directory: the owner starts the server
	// in any directory. Resolve to an absolute path once, so no later component
	// depends on the process's cwd.
	project, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	project, err = filepath.Abs(project)
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	slog.Info("starting sssh", "project", project)

	// The registry is the single source of truth; it is shared with the front
	// door and the control plane.
	reg := registry.New()

	// The front door bridges accepted sessions into the sandbox, so it needs the
	// VM handle to reach the guest sshd once the VM has booted.
	vmm := vm.New(project)

	// Public ingress: the tunnel opens the ngrok endpoint the front door serves
	// guests on, and the control plane reads its URL for the guest connect
	// string. Started before the front door so its listener is ready.
	tun := tunnel.New()

	// The front door serves the owner over LAN and guests over the tunnel's
	// public listener, and ends the session when the owner disconnects.
	fd := frontdoor.New(reg, vmm, tun, cancel)

	// The control plane reads the guest connect URL from the tunnel and the owner
	// connect address from the front door, drives the registry, and calls cancel
	// to end the session on quit.
	ctrl := control.New(reg, fd, tun, cancel)

	// Managers start in dependency order: state, then the sandbox, the commit
	// helper's writer (which needs the sandbox's share path), then public
	// ingress, the front door that accepts connections, and finally the owner's
	// control plane. Teardown runs in reverse (see teardown).
	managers := []lifecycle.Manager{
		reg,
		vmm,
		coauthor.NewWriter(reg, vmm),
		tun,
		fd,
		ctrl,
	}

	// Start managers one at a time. If any fails, tear down the ones already
	// started and abort. A failed boot must leave nothing running or exposed.
	started, err := startManagers(ctx, managers)
	if err != nil {
		teardown(started)
		return err
	}

	slog.Info("sssh ready")

	// Block until a signal cancels the root context.
	<-ctx.Done()
	slog.Info("shutdown signal received, tearing down")

	teardown(started)
	return nil
}

// startManagers starts managers in order and returns those that came up. On the
// first failure it stops and returns the started set, so the caller can tear it
// down, along with the error.
func startManagers(ctx context.Context, managers []lifecycle.Manager) ([]lifecycle.Manager, error) {
	started := make([]lifecycle.Manager, 0, len(managers))
	for _, m := range managers {
		slog.Info("starting manager", "manager", m.Name())
		if err := m.Start(ctx); err != nil {
			return started, fmt.Errorf("start %s: %w", m.Name(), err)
		}
		started = append(started, m)
	}
	return started, nil
}

// teardown stops managers in reverse of their start order, so each subsystem
// is unwound before the ones it was started on top of. Stop errors are logged
// but never abort teardown: every manager must get a chance to release its
// resources.
func teardown(started []lifecycle.Manager) {
	// Teardown needs a live context because the root one is already cancelled by
	// the signal; give it a bounded deadline so cleanup can't hang forever.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	for i := len(started) - 1; i >= 0; i-- {
		m := started[i]
		slog.Info("stopping manager", "manager", m.Name())
		if err := m.Stop(ctx); err != nil {
			slog.Error("manager stop failed", "manager", m.Name(), "err", err)
		}
	}
}
