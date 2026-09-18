// Package frontdoor is the SSH front door. It is a wish server with two
// listeners: ngrok for guests and loopback/LAN for the owner. It runs the join
// ceremony, then bridges accepted sessions into the VM's shared tmux. Guests
// only ever touch this front door.
package frontdoor

import (
	"context"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Manager runs the SSH front door and bridges accepted sessions.
type Manager interface {
	lifecycle.Manager
}

// stub is the no-op implementation.
type stub struct{}

// New returns a no-op front door. The wish listeners are added later, along with
// the registry and VM handle it will depend on.
func New() Manager { return &stub{} }

func (s *stub) Name() string                { return "frontdoor" }
func (s *stub) Start(context.Context) error { return nil }
func (s *stub) Stop(context.Context) error  { return nil }
