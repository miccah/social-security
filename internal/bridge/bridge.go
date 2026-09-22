// Package bridge pipes one accepted connection's PTY to the VM's shared tmux
// session over the host-only network, effectively
// `ssh -t <vm> tmux attach -t pairing`. The front door creates one bridge per
// accepted guest and tears it down on kick or teardown.
//
// Unlike the other subsystems a bridge is per-connection, not a long-lived
// managed service, so it is deliberately not part of the entrypoint's
// startup/teardown set.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"

	"github.com/miccah/social-security/internal/vm"
)

// attachCmd joins the shared pairing session. new-session -A attaches when the
// session exists and creates it otherwise, so a connection that races the VM's
// pairing service still lands in the same shared session.
const attachCmd = "tmux new-session -A -s pairing"

// dialTimeout bounds the host to guest ssh dial.
const dialTimeout = 10 * time.Second

// Bridge connects a single accepted session to the VM's shared tmux.
type Bridge interface {
	// Run copies bytes in both directions until ctx is cancelled or either end
	// closes. It blocks for the lifetime of the connection.
	Run(ctx context.Context) error
}

type bridge struct {
	target  vm.Target
	session ssh.Session
	stdin   io.Reader
}

// New returns a bridge that attaches the guest session to the VM's pairing tmux.
// The VM's output is written to the session; guest input is read from stdin,
// which the caller may wrap (the raw session is the common case).
func New(target vm.Target, s ssh.Session, stdin io.Reader) Bridge {
	return &bridge{target: target, session: s, stdin: stdin}
}

// Run dials the guest sshd, attaches to the shared pairing tmux, and copies
// bytes in both directions until ctx is cancelled or either end closes. A normal
// detach, close, or cancellation returns nil; only an unexpected transport error
// is surfaced.
func (b *bridge) Run(ctx context.Context) error {
	cfg := &gossh.ClientConfig{
		User: b.target.User,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(b.target.Signer)},
		// The guest sshd is our own VM behind a loopback forward with an ephemeral
		// host key, so there is nothing to pin.
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}
	client, err := gossh.Dial("tcp", b.target.Addr, cfg)
	if err != nil {
		return fmt.Errorf("dial sandbox ssh: %w", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open sandbox session: %w", err)
	}
	defer sess.Close()

	// done bounds the helper goroutines to Run's lifetime.
	done := make(chan struct{})
	defer close(done)

	// Mirror the guest's terminal so tmux renders correctly, and forward resizes.
	if pty, winCh, ok := b.session.Pty(); ok {
		if err := sess.RequestPty(pty.Term, pty.Window.Height, pty.Window.Width, gossh.TerminalModes{}); err != nil {
			return fmt.Errorf("request sandbox pty: %w", err)
		}
		go func() {
			for {
				select {
				case <-done:
					return
				case w, ok := <-winCh:
					if !ok {
						return
					}
					sess.WindowChange(w.Height, w.Width)
				}
			}
		}()
	}

	sess.Stdin = b.stdin
	sess.Stdout = b.session
	sess.Stderr = b.session.Stderr()

	if err := sess.Start(attachCmd); err != nil {
		return fmt.Errorf("start pairing attach: %w", err)
	}

	// Close the guest-facing session when the connection is cancelled (owner
	// teardown or kick) so Wait can't block on a dead client.
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()

	// A detach, a closed connection, or a cancelled context all end the session
	// normally; only an unexpected transport error is worth surfacing.
	err = sess.Wait()
	if err == nil || ctx.Err() != nil {
		return nil
	}
	var exitErr *gossh.ExitError
	var missingErr *gossh.ExitMissingError
	if errors.As(err, &exitErr) || errors.As(err, &missingErr) || errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("pairing session: %w", err)
}
