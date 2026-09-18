// Package registry holds sssh's session state: pending join requests, active
// guests, and owner presence. It is the single source of truth the front door
// and control plane coordinate through.
package registry

import (
	"context"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Registry is the concurrency-safe store of session state. It embeds
// lifecycle.Manager so it participates in ordered startup/teardown even though,
// being purely in-memory, its Start and Stop are no-ops.
type Registry interface {
	lifecycle.Manager
}

// stub is the no-op implementation.
type stub struct{}

// New returns a no-op registry; the real state store is not implemented yet.
func New() Registry { return &stub{} }

func (s *stub) Name() string { return "registry" }

// Start is a no-op: in-memory state needs no boot step. The method exists so
// the registry is managed uniformly alongside the other subsystems.
func (s *stub) Start(context.Context) error { return nil }

// Stop is a no-op for the same reason.
func (s *stub) Stop(context.Context) error { return nil }
