package registry

import (
	"sync"
	"testing"
	"time"
)

// The registry tracks the active set and owner presence, and dropping a session
// removes its entry and clears owner presence when the last owner leaves.
func TestActiveSetAndOwnerPresence(t *testing.T) {
	r := New()
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0", r.Count())
	}
	if r.OwnerPresent() {
		t.Fatal("no owner should be present initially")
	}

	owner := r.Add("10.0.0.1:2200", true)
	time.Sleep(time.Millisecond) // distinct ConnectedAt for stable ordering
	guest := r.Add("10.0.0.2:2200", false)

	if owner.ID == guest.ID {
		t.Fatal("session IDs must be unique")
	}
	if r.Count() != 2 {
		t.Fatalf("Count = %d, want 2", r.Count())
	}
	if !r.OwnerPresent() {
		t.Fatal("owner should be present")
	}

	active := r.Active()
	if len(active) != 2 || active[0].ID != owner.ID || active[1].ID != guest.ID {
		t.Fatalf("Active = %+v, want owner then guest", active)
	}

	r.Remove(owner.ID)
	if r.Count() != 1 {
		t.Fatalf("Count = %d, want 1 after owner leaves", r.Count())
	}
	if r.OwnerPresent() {
		t.Fatal("owner presence should clear once the owner disconnects")
	}

	r.Remove(guest.ID)
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0", r.Count())
	}
	r.Remove("no-such-id") // must not panic
}

// Concurrent access is safe (run under -race).
func TestConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := r.Add("10.0.0.9:2200", false)
			_ = r.Active()
			_ = r.Count()
			_ = r.OwnerPresent()
			r.Remove(s.ID)
		}()
	}
	wg.Wait()
	if r.Count() != 0 {
		t.Fatalf("Count = %d, want 0 after all disconnect", r.Count())
	}
}
