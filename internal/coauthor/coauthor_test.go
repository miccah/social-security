package coauthor

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Order appends new users in connection order and holds that order steady as
// users disconnect and reconnect, so lines keep their position.
func TestOrderStable(t *testing.T) {
	s := &Store{}
	if got := s.Order([]string{"alice", "bob"}); !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("initial order = %v", got)
	}
	// bob drops: only alice is credited, but bob keeps his slot in the store.
	if got := s.Order([]string{"alice"}); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Fatalf("after bob leaves = %v", got)
	}
	// carol joins then bob returns: bob precedes carol by original order.
	if got := s.Order([]string{"alice", "carol", "bob"}); !reflect.DeepEqual(got, []string{"alice", "bob", "carol"}) {
		t.Fatalf("after carol joins and bob returns = %v", got)
	}
}

// Render emits a real trailer for a known email and a placeholder for an unknown
// one, appended as the final paragraph.
func TestRenderKnownAndUnknown(t *testing.T) {
	s := &Store{entries: []entry{{ssh: "alice", name: "Alice", email: "alice@x.io"}, {ssh: "bob", name: "bob"}}}
	got := s.Render([]string{"alice", "bob"}, "subject\n")
	want := "subject\n\nCo-authored-by: Alice <alice@x.io>\nCo-authored-by: bob <add your email>\n"
	if got != want {
		t.Fatalf("Render =\n%q\nwant\n%q", got, want)
	}
}

// Render is idempotent: re-running over its own output rewrites the same block
// rather than duplicating it, and dropping a user drops their line.
func TestRenderIdempotentAndTracksDrop(t *testing.T) {
	s := &Store{entries: []entry{{ssh: "alice", name: "alice"}, {ssh: "bob", name: "bob"}}}
	first := s.Render([]string{"alice", "bob"}, "subject\n")
	if again := s.Render([]string{"alice", "bob"}, first); again != first {
		t.Fatalf("not idempotent:\n%q\nvs\n%q", again, first)
	}
	dropped := s.Render([]string{"alice"}, first)
	if strings.Contains(dropped, "bob") {
		t.Fatalf("bob should be dropped:\n%q", dropped)
	}
	if !strings.Contains(dropped, "Co-authored-by: alice") {
		t.Fatalf("alice should remain:\n%q", dropped)
	}
}

// Render places the block at the end of the body, above git's comment and diff
// tail (as with `git commit -v`), and ignores a Co-authored-by line inside the
// diff.
func TestRenderPlacesBlockBeforeTail(t *testing.T) {
	s := &Store{entries: []entry{{ssh: "alice", name: "alice"}}}
	msg := "subject\n\nbody\n\n# Please enter the commit message\n# ---- >8 ----\ndiff --git a/f b/f\n+Co-authored-by: ghost <ghost@x.io>\n"
	got := s.Render([]string{"alice"}, msg)
	want := "subject\n\nbody\n\nCo-authored-by: alice <add your email>\n# Please enter the commit message\n# ---- >8 ----\ndiff --git a/f b/f\n+Co-authored-by: ghost <ghost@x.io>\n"
	if got != want {
		t.Fatalf("Render =\n%q\nwant\n%q", got, want)
	}
}

// On amend, Render replaces the existing trailers in place without duplicating
// them or disturbing the subject, even with a comment and diff tail present.
func TestRenderAmendReplacesInPlace(t *testing.T) {
	s := &Store{entries: []entry{{ssh: "alice", name: "Alice", email: "alice@x.io"}}}
	msg := "subject\n\nCo-authored-by: Alice <alice@x.io>\n# comment\ndiff --git\n"
	got := s.Render([]string{"alice"}, msg)
	if want := "subject\n\nCo-authored-by: Alice <alice@x.io>\n# comment\ndiff --git\n"; got != want {
		t.Fatalf("amend Render =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(got, "Co-authored-by"); n != 1 {
		t.Fatalf("expected exactly one trailer, got %d:\n%s", n, got)
	}
}

// Learn maps each co-author line to a user by position, recording a filled-in
// email and leaving an untouched placeholder unknown.
func TestLearnByPosition(t *testing.T) {
	s := &Store{entries: []entry{{ssh: "alice", name: "alice"}, {ssh: "bob", name: "bob"}}}
	order := s.Order([]string{"alice", "bob"})
	// The user renamed and set an email on the first line; left the second alone.
	msg := "subject\n\nCo-authored-by: Alice Smith <alice@x.io>\nCo-authored-by: bob <add your email>\n"
	s.Learn(order, msg)
	if e := s.get("alice"); e.name != "Alice Smith" || e.email != "alice@x.io" {
		t.Fatalf("alice = %+v, want Alice Smith/alice@x.io", *e)
	}
	if e := s.get("bob"); e.email != "" {
		t.Fatalf("bob email = %q, want empty (placeholder untouched)", e.email)
	}
}

// A first commit round-trips: an unknown user fills their email, and the next
// commit renders it automatically.
func TestRenderLearnRoundTrip(t *testing.T) {
	s := &Store{}
	order := s.Order([]string{"alice"})
	first := s.Render(order, "subject\n")
	filled := strings.Replace(first, "Co-authored-by: alice <add your email>", "Co-authored-by: alice <alice@x.io>", 1)
	s.Learn(order, filled)
	next := s.Render(s.Order([]string{"alice"}), "next subject\n")
	if !strings.Contains(next, "Co-authored-by: alice <alice@x.io>") {
		t.Fatalf("email not remembered:\n%q", next)
	}
}

// Load and Save round-trip the store, preserving order and empty emails; a
// missing file loads empty.
func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "coauthors")
	if s, err := Load(path); err != nil || len(s.entries) != 0 {
		t.Fatalf("Load(missing) = %v, %v; want empty, nil", s, err)
	}
	s := &Store{entries: []entry{{ssh: "alice", name: "Alice", email: "alice@x.io"}, {ssh: "bob", name: "bob"}}}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.entries, s.entries) {
		t.Fatalf("Load = %+v, want %+v", got.entries, s.entries)
	}
}

// ReadList trims blank lines and reports a missing file as an empty list.
func TestReadList(t *testing.T) {
	if got, err := ReadList(filepath.Join(t.TempDir(), "absent")); err != nil || got != nil {
		t.Fatalf("ReadList(missing) = %v, %v; want nil, nil", got, err)
	}
	path := filepath.Join(t.TempDir(), "connected")
	if err := writeList(path, []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadList(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alice", "bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadList = %v, want %v", got, want)
	}
}
