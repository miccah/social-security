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

// ErrOwnerAbsent is returned by Resolve when accepting a request while the owner
// is not in the session. Guests are only ever admitted to a session the owner is
// already in.
var ErrOwnerAbsent = errors.New("registry: owner is not in the session")

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
	// keeps it claimed for the coming Activate. Accepting while the owner is absent
	// returns ErrOwnerAbsent and leaves the request pending. An unknown ID returns
	// an error.
	Resolve(id string, d Decision) error
	// CancelPending drops a still-pending request and frees its username. It is a
	// no-op once the request is gone (already resolved or cancelled).
	CancelPending(id string)
	// Activate promotes an accepted request to an active session. onKick is called
	// by Kick to close the connection; the front door's teardown then frees the
	// username via RemoveActive.
	Activate(req *Request, onKick func()) Session
	// RemoveActive drops an active session and frees its username.
	RemoveActive(id string)
	// Kick closes an active session by invoking its on-kick callback. An unknown
	// ID returns an error.
	Kick(id string) error

	// ClaimOwner marks the caller as the owner when the slot is free, returning
	// true. Once claimed, later callers get false and are treated as guests.
	ClaimOwner() bool
	// ReleaseOwner frees the owner slot when the owner disconnects.
	ReleaseOwner()
	// OwnerPresent reports whether the owner slot is claimed.
	OwnerPresent() bool

	// Pending returns a snapshot of the waiting requests, oldest first.
	Pending() []Request
	// Active returns a snapshot of the active sessions, oldest first.
	Active() []Session
	// Count returns the number of active sessions.
	Count() int
	// Events signals that pending or active state changed. Sends are coalesced, so
	// a receiver that misses one still sees the latest state on its next read.
	Events() <-chan struct{}
}

type registry struct {
	mu      sync.Mutex
	nextID  uint64
	names   map[string]struct{} // usernames claimed by a pending request or an active session
	pending map[string]*Request
	active  map[string]Session
	kicks   map[string]func() // per active session, invoked by Kick
	owner   bool              // true once a connection has claimed the owner slot
	events  chan struct{}
}

// New returns an empty session registry.
func New() Registry {
	return &registry{
		names:   make(map[string]struct{}),
		pending: make(map[string]*Request),
		active:  make(map[string]Session),
		kicks:   make(map[string]func()),
		events:  make(chan struct{}, 1),
	}
}

// Name identifies the registry in lifecycle logs.
func (r *registry) Name() string { return "registry" }

// Start is a no-op: in-memory state needs no boot step. The method exists so the
// registry is managed uniformly alongside the other subsystems.
func (r *registry) Start(context.Context) error { return nil }

// Stop is a no-op for the same reason.
func (r *registry) Stop(context.Context) error { return nil }

// AddPending claims the username under the lock, then mints a request with a
// fresh ID, stores it, and notifies watchers. It returns ErrUsernameTaken when
// the name is already held by a pending or active session.
func (r *registry) AddPending(username, ssn, remoteAddr string) (*Request, error) {
	r.mu.Lock()
	if _, taken := r.names[username]; taken {
		r.mu.Unlock()
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
	r.mu.Unlock()

	r.notify()
	return req, nil
}

// Resolve removes the pending request and hands the decision to the waiting
// handler over the buffered channel. Accepting requires the owner present and
// keeps the username claimed for Activate; declining frees it. It errors on an
// unknown ID and returns ErrOwnerAbsent when accepting with no owner.
func (r *registry) Resolve(id string, d Decision) error {
	r.mu.Lock()
	req, ok := r.pending[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("registry: no pending request %q", id)
	}
	// A guest may only be admitted to a session the owner is already in. Accepting
	// while the owner is absent leaves the request pending, so it can be accepted
	// once the owner joins.
	if d == Accept && !r.owner {
		r.mu.Unlock()
		return ErrOwnerAbsent
	}
	delete(r.pending, id)
	if d == Decline {
		delete(r.names, req.Username)
	}
	req.decision <- d // buffered(1): never blocks, even if the handler has gone
	r.mu.Unlock()

	r.notify()
	return nil
}

// CancelPending drops a still-pending request and frees its username, notifying
// watchers only when something was removed.
func (r *registry) CancelPending(id string) {
	r.mu.Lock()
	req, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
		delete(r.names, req.Username)
	}
	r.mu.Unlock()

	if ok {
		r.notify()
	}
}

// Activate records the accepted request as an active session, keeping its
// username claimed from AddPending and registering the on-kick callback.
func (r *registry) Activate(req *Request, onKick func()) Session {
	r.mu.Lock()
	s := Session{
		ID:          req.ID,
		Username:    req.Username,
		RemoteAddr:  req.RemoteAddr,
		ConnectedAt: time.Now(),
	}
	r.active[s.ID] = s // the username stays claimed from AddPending
	r.kicks[s.ID] = onKick
	r.mu.Unlock()

	r.notify()
	return s
}

// RemoveActive drops an active session, releasing its username and kick callback,
// and notifies watchers only when something was removed.
func (r *registry) RemoveActive(id string) {
	r.mu.Lock()
	s, ok := r.active[id]
	if ok {
		delete(r.active, id)
		delete(r.kicks, id)
		delete(r.names, s.Username)
	}
	r.mu.Unlock()

	if ok {
		r.notify()
	}
}

// Kick invokes the session's on-kick callback outside the lock to close its
// connection. It errors on an unknown ID.
func (r *registry) Kick(id string) error {
	r.mu.Lock()
	onKick, ok := r.kicks[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("registry: no active session %q", id)
	}
	onKick() // closes the connection; the front door frees the name via RemoveActive
	r.notify()
	return nil
}

// ClaimOwner takes the owner slot when it is free, returning whether this caller
// got it, and notifies watchers on success.
func (r *registry) ClaimOwner() bool {
	r.mu.Lock()
	claimed := !r.owner
	if claimed {
		r.owner = true
	}
	r.mu.Unlock()

	if claimed {
		r.notify()
	}
	return claimed
}

// ReleaseOwner frees the owner slot and notifies watchers.
func (r *registry) ReleaseOwner() {
	r.mu.Lock()
	r.owner = false
	r.mu.Unlock()

	r.notify()
}

// OwnerPresent reports whether the owner slot is currently claimed.
func (r *registry) OwnerPresent() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner
}

// Pending returns a snapshot of the waiting requests, oldest first.
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

// Active returns a snapshot of the active sessions, oldest first.
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

// Count returns the number of active sessions.
func (r *registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

// Events returns the coalesced state-change channel watchers select on.
func (r *registry) Events() <-chan struct{} { return r.events }

// notify coalesces a state-change signal onto the events channel.
func (r *registry) notify() {
	select {
	case r.events <- struct{}{}:
	default:
	}
}
