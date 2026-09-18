// Package registry holds sssh's session state: pending join requests, active
// guests, and owner presence. It is the single source of truth the front door
// and control plane coordinate through.
package registry

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Session is one connection the front door has bridged into the shared tmux.
type Session struct {
	ID          string // unique per connection, for logs and the control plane
	RemoteAddr  string // client address
	Owner       bool   // recognized as an owner-side (LAN) connection
	ConnectedAt time.Time
}

// Registry is the concurrency-safe store of session state: the active set and
// owner presence. It embeds lifecycle.Manager so it participates in ordered
// startup/teardown even though, being purely in-memory, Start and Stop are
// no-ops.
type Registry interface {
	lifecycle.Manager

	// Add registers a newly connected session and returns it with a unique ID.
	Add(remoteAddr string, owner bool) Session
	// Remove drops a session by ID; an unknown ID is ignored.
	Remove(id string)
	// Active returns a snapshot of the current sessions, oldest first.
	Active() []Session
	// Count returns the number of active sessions.
	Count() int
	// OwnerPresent reports whether any owner-side session is connected.
	OwnerPresent() bool
}

type registry struct {
	mu     sync.Mutex
	nextID uint64
	active map[string]Session // keyed by Session.ID
}

// New returns an empty session registry.
func New() Registry {
	return &registry{active: make(map[string]Session)}
}

func (r *registry) Name() string { return "registry" }

// Start is a no-op: in-memory state needs no boot step. The method exists so the
// registry is managed uniformly alongside the other subsystems.
func (r *registry) Start(context.Context) error { return nil }

// Stop is a no-op for the same reason.
func (r *registry) Stop(context.Context) error { return nil }

func (r *registry) Add(remoteAddr string, owner bool) Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	s := Session{
		ID:          strconv.FormatUint(r.nextID, 10),
		RemoteAddr:  remoteAddr,
		Owner:       owner,
		ConnectedAt: time.Now(),
	}
	r.active[s.ID] = s
	return s
}

func (r *registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, id)
}

func (r *registry) Active() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Session, 0, len(r.active))
	for _, s := range r.active {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

func (r *registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

func (r *registry) OwnerPresent() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.active {
		if s.Owner {
			return true
		}
	}
	return false
}
