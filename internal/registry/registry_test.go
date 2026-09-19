package registry

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A join request lands in pending, an accept promotes it to active, and removing
// the active session frees the username for reuse.
func TestPendingAcceptActivate(t *testing.T) {
	r := New()
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0", r.Count())
	}

	req, err := r.AddPending("alice", "123-45-6789", "10.0.0.1:2200")
	if err != nil {
		t.Fatal(err)
	}
	pending := r.Pending()
	if len(pending) != 1 || pending[0].Username != "alice" || pending[0].SSN != "123-45-6789" {
		t.Fatalf("Pending = %+v, want one request for alice", pending)
	}

	if err := r.Resolve(req.ID, Accept); err != nil {
		t.Fatal(err)
	}
	if d := <-req.Decision(); d != Accept {
		t.Fatalf("decision = %v, want Accept", d)
	}
	if len(r.Pending()) != 0 {
		t.Fatal("request should leave pending once resolved")
	}

	sess := r.Activate(req, func() {})
	if sess.Username != "alice" {
		t.Fatalf("session username = %q, want alice", sess.Username)
	}
	if r.Count() != 1 {
		t.Fatalf("Count = %d, want 1", r.Count())
	}

	r.RemoveActive(sess.ID)
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0 after disconnect", r.Count())
	}
	// The username is free again.
	if _, err := r.AddPending("alice", "x", "y"); err != nil {
		t.Fatalf("username should be free after the session ends: %v", err)
	}
}

// A username claimed by a pending or active entry is rejected until it is freed.
func TestUsernameUniqueness(t *testing.T) {
	r := New()
	if _, err := r.AddPending("bob", "1", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddPending("bob", "2", "b"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
}

// Declining delivers the decision and frees the username.
func TestDeclineFreesUsername(t *testing.T) {
	r := New()
	req, err := r.AddPending("carol", "1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve(req.ID, Decline); err != nil {
		t.Fatal(err)
	}
	if d := <-req.Decision(); d != Decline {
		t.Fatalf("decision = %v, want Decline", d)
	}
	if _, err := r.AddPending("carol", "2", "b"); err != nil {
		t.Fatalf("username should be free after decline: %v", err)
	}
}

// Cancelling a pending request drops it and frees the username; Resolve on an
// unknown request errors.
func TestCancelPendingAndUnknownResolve(t *testing.T) {
	r := New()
	req, err := r.AddPending("dave", "1", "a")
	if err != nil {
		t.Fatal(err)
	}
	r.CancelPending(req.ID)
	if len(r.Pending()) != 0 {
		t.Fatal("request should be gone after cancel")
	}
	if _, err := r.AddPending("dave", "2", "b"); err != nil {
		t.Fatalf("username should be free after cancel: %v", err)
	}
	if err := r.Resolve("no-such-id", Accept); err == nil {
		t.Fatal("Resolve of an unknown request should error")
	}
	r.CancelPending("no-such-id") // must not panic
}

// Kick invokes the session's on-kick callback; an unknown session errors.
func TestKickInvokesCallback(t *testing.T) {
	r := New()
	req, _ := r.AddPending("erin", "1", "a")
	if err := r.Resolve(req.ID, Accept); err != nil {
		t.Fatal(err)
	}
	<-req.Decision()
	called := make(chan struct{}, 1)
	sess := r.Activate(req, func() { called <- struct{}{} })

	if err := r.Kick(sess.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	default:
		t.Fatal("Kick should invoke onKick")
	}
	if err := r.Kick("no-such-id"); err == nil {
		t.Fatal("Kick of an unknown session should error")
	}
}

// A state change signals the events channel.
func TestEventsSignalOnChange(t *testing.T) {
	r := New()
	if _, err := r.AddPending("frank", "1", "a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Events():
	case <-time.After(time.Second):
		t.Fatal("AddPending should signal Events")
	}
}

// Pending is returned oldest first.
func TestPendingOrder(t *testing.T) {
	r := New()
	first, _ := r.AddPending("a", "1", "x")
	time.Sleep(time.Millisecond)
	second, _ := r.AddPending("b", "2", "y")
	p := r.Pending()
	if len(p) != 2 || p[0].ID != first.ID || p[1].ID != second.ID {
		t.Fatalf("Pending order = %+v, want first then second", p)
	}
}

// Concurrent access is safe (run under -race).
func TestConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := "user-" + strconv.Itoa(n)
			req, err := r.AddPending(name, "ssn", "addr")
			if err != nil {
				return
			}
			_ = r.Pending()
			_ = r.Active()
			_ = r.Count()
			if err := r.Resolve(req.ID, Accept); err == nil {
				<-req.Decision()
				sess := r.Activate(req, func() {})
				r.RemoveActive(sess.ID)
			}
		}(i)
	}
	wg.Wait()
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0 after all disconnect", r.Count())
	}
}
