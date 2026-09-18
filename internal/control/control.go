// Package control is the owner's control plane: an interactive Bubble Tea UI in
// the launching terminal. It prints the guest and owner connect strings, shows
// connected/waiting counts and the pending request list, and handles
// accept/decline/kick. It runs as a separate host-side process that guests never
// connect to, so it is invisible to and unreachable by them.
package control

import (
	"context"
	"log/slog"

	"github.com/miccah/social-security/internal/lifecycle"
	"github.com/miccah/social-security/internal/registry"
)

// Plane is the owner's control surface in the launching terminal.
type Plane interface {
	lifecycle.Manager
}

// stub is the no-op implementation. The plane takes the registry so it can
// read state and resolves join requests against it.
type stub struct {
	reg registry.Registry
}

// New returns a no-op control plane bound to the session registry.
func New(reg registry.Registry) Plane { return &stub{reg: reg} }

func (s *stub) Name() string { return "control" }

// Start will subscribe to the registry's event stream and render the UI. The
// stub only confirms the registry was wired in.
func (s *stub) Start(context.Context) error {
	slog.Debug("control: UI disabled (stub)", "registry_bound", s.reg != nil)
	return nil
}

func (s *stub) Stop(context.Context) error { return nil }
