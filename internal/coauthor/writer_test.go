package coauthor

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/miccah/social-security/internal/registry"
)

// fakeShare provides a fixed share directory.
type fakeShare struct{ dir string }

func (f fakeShare) SharePath() (string, error) { return f.dir, nil }

// activate registers and activates a guest, returning its session id.
func activate(t *testing.T, reg registry.Registry, name string) string {
	t.Helper()
	req, err := reg.AddPending(name, "ssn", "addr")
	if err != nil {
		t.Fatal(err)
	}
	return reg.Activate(req, func() {}).ID
}

// waitList polls path until it holds want, or fails after a timeout.
func waitList(t *testing.T, path string, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := ReadList(path)
		if err == nil && reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connected file = %v, want %v (err %v)", got, want, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The writer publishes the initial (empty) set, then tracks activations and
// removals as the registry changes.
func TestWriterTracksActiveUsers(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New()
	w := NewWriter(reg, fakeShare{dir})
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer w.Stop(context.Background())

	path := filepath.Join(dir, connectedFile)
	waitList(t, path, nil)

	alice := activate(t, reg, "alice")
	waitList(t, path, []string{"alice"})

	activate(t, reg, "bob")
	waitList(t, path, []string{"alice", "bob"})

	reg.RemoveActive(alice)
	waitList(t, path, []string{"bob"})
}

// Start fails when the share path is unavailable, so a misordered boot surfaces
// the error rather than silently publishing nowhere.
func TestWriterStartNoShare(t *testing.T) {
	w := NewWriter(registry.New(), errShare{})
	if err := w.Start(context.Background()); err == nil {
		t.Fatal("Start should fail when the share path is unavailable")
	}
}

// errShare always fails to provide a share directory.
type errShare struct{}

func (errShare) SharePath() (string, error) { return "", context.DeadlineExceeded }
