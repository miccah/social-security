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
)

// errNotImplemented marks the stub so an accidental early call fails loudly
// instead of silently succeeding.
var errNotImplemented = errors.New("bridge: not implemented")

// Bridge connects a single accepted session to the VM's shared tmux.
type Bridge interface {
	// Run copies bytes in both directions until ctx is cancelled or either end
	// closes. It blocks for the lifetime of the connection.
	Run(ctx context.Context) error
}

// stub is the no-op implementation.
type stub struct{}

// New returns a no-op bridge; wiring it to the VM's tmux over host-only ssh is
// not implemented yet.
func New() Bridge { return &stub{} }

func (s *stub) Run(context.Context) error { return errNotImplemented }
