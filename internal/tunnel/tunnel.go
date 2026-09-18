// Package tunnel manages the public ingress: an ngrok TCP endpoint exposed as a
// net.Listener on the host. It surfaces the public address to the control plane
// and closes on teardown. ngrok is the only public path to the front door.
package tunnel

import (
	"context"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Manager owns the ngrok tunnel lifecycle.
type Manager interface {
	lifecycle.Manager
}

// stub is the no-op implementation.
type stub struct{}

// New returns a no-op tunnel manager; the ngrok listener is not implemented yet.
func New() Manager { return &stub{} }

func (s *stub) Name() string                { return "tunnel" }
func (s *stub) Start(context.Context) error { return nil }
func (s *stub) Stop(context.Context) error  { return nil }
