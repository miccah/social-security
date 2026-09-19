// Package registry holds sssh's session state: pending join requests and active
// guests. It is the single source of truth the front door and control plane
// coordinate through, and it enforces username uniqueness across both sets.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Decision is the owner's ruling on a pending join request.
type Decision int

const (
	Accept Decision = iota
	Decline
)

// ErrUsernameTaken is returned by AddPending when the username is already claimed
// by a pending request or an active session.
var ErrUsernameTaken = errors.New("registry: username already in use")

// Request is a join request awaiting the owner's decision.
type Request struct {
	ID         string
	Username   string
	SSN        string
	RemoteAddr string
	CreatedAt  time.Time
	decision   chan Decision
}

// Decision returns the channel the front-door handler blocks on. Resolve sends
// exactly one value; the channel is buffered so Resolve never blocks.
func (r *Request) Decision() <-chan Decision { return r.decision }

// Session is an accepted, bridged connection.
type Session struct {
	ID          string
	Username    string
	RemoteAddr  string
	ConnectedAt time.Time
}

// Registry is the concurrency-safe store of session state. It embeds
// lifecycle.Manager so it participates in ordered startup/teardown even though,
// being purely in-memory, Start and Stop are no-ops.
type Registry interface {
	lifecycle.Manager

	// AddPending registers a join request, claiming the username against both the
	// pending and active sets. It returns ErrUsernameTaken if the name is in use.
	AddPending(username, ssn, remoteAddr string) (*Request, error)
	// Resolve delivers the owner's decision to the blocked handler and removes the
	// request from pending. A declined request frees its username; an accepted one
	// keeps it claimed for the coming Activate. An unknown ID returns an error.
	Resolve(id string, d Decision) error
	// CancelPending drops a still-pending request and frees its username. It is a
	// no-op once the request is gone (already resolved or cancelled).
	CancelPending(id string)
	// Activate promotes an accepted request to an active session.
	Activate(req *Request) Session
	// RemoveActive drops an active session and frees its username.
	RemoveActive(id string)

	// Pending returns a snapshot of the waiting requests, oldest first.
	Pending() []Request
	// Active returns a snapshot of the active sessions, oldest first.
	Active() []Session
	// Count returns the number of active sessions.
	Count() int
}

type registry struct {
	mu      sync.Mutex
	nextID  uint64
	names   map[string]struct{} // usernames claimed by a pending request or an active session
	pending map[string]*Request
	active  map[string]Session
}

// New returns an empty session registry.
func New() Registry {
	return &registry{
		names:   make(map[string]struct{}),
		pending: make(map[string]*Request),
		active:  make(map[string]Session),
	}
}

func (r *registry) Name() string { return "registry" }

// Start is a no-op: in-memory state needs no boot step. The method exists so the
// registry is managed uniformly alongside the other subsystems.
func (r *registry) Start(context.Context) error { return nil }

// Stop is a no-op for the same reason.
func (r *registry) Stop(context.Context) error { return nil }

func (r *registry) AddPending(username, ssn, remoteAddr string) (*Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.names[username]; taken {
		return nil, ErrUsernameTaken
	}
	r.nextID++
	req := &Request{
		ID:         strconv.FormatUint(r.nextID, 10),
		Username:   username,
		SSN:        ssn,
		RemoteAddr: remoteAddr,
		CreatedAt:  time.Now(),
		decision:   make(chan Decision, 1),
	}
	r.names[username] = struct{}{}
	r.pending[req.ID] = req
	return req, nil
}

func (r *registry) Resolve(id string, d Decision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.pending[id]
	if !ok {
		return fmt.Errorf("registry: no pending request %q", id)
	}
	delete(r.pending, id)
	if d == Decline {
		delete(r.names, req.Username)
	}
	req.decision <- d // buffered(1): never blocks, even if the handler has gone
	return nil
}

func (r *registry) CancelPending(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req, ok := r.pending[id]; ok {
		delete(r.pending, id)
		delete(r.names, req.Username)
	}
}

func (r *registry) Activate(req *Request) Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Session{
		ID:          req.ID,
		Username:    req.Username,
		RemoteAddr:  req.RemoteAddr,
		ConnectedAt: time.Now(),
	}
	r.active[s.ID] = s // the username stays claimed from AddPending
	return s
}

func (r *registry) RemoveActive(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.active[id]; ok {
		delete(r.active, id)
		delete(r.names, s.Username)
	}
}

func (r *registry) Pending() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Request, 0, len(r.pending))
	for _, req := range r.pending {
		out = append(out, *req)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
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
