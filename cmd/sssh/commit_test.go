package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miccah/social-security/internal/coauthor"
)

// setupCommitEnv points the helper at temp files and returns their paths.
func setupCommitEnv(t *testing.T) (connected, coauthors string) {
	t.Helper()
	dir := t.TempDir()
	connected = filepath.Join(dir, "connected")
	coauthors = filepath.Join(dir, "coauthors")
	t.Setenv(connectedFileEnv, connected)
	t.Setenv(coauthorsFileEnv, coauthors)
	return connected, coauthors
}

// prepare appends a real trailer for a known email and a placeholder for an
// unknown one, in the connected order.
func TestCommitPrepare(t *testing.T) {
	connected, coauthors := setupCommitEnv(t)
	if err := os.WriteFile(connected, []byte("alice\nbob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := &coauthor.Store{}
	seed.Order([]string{"alice"})
	seed.Learn([]string{"alice"}, "x\n\nCo-authored-by: Alice <alice@x.io>\n")
	if err := seed.Save(coauthors); err != nil {
		t.Fatal(err)
	}
	msg := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(msg, []byte("subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := commitMain([]string{"prepare", msg}); code != 0 {
		t.Fatalf("prepare exit = %d, want 0", code)
	}
	got := readFile(t, msg)
	if !strings.Contains(got, "Co-authored-by: Alice <alice@x.io>") {
		t.Errorf("missing alice trailer in:\n%s", got)
	}
	if !strings.Contains(got, "Co-authored-by: bob <add your email>") {
		t.Errorf("missing bob placeholder in:\n%s", got)
	}
	if strings.Contains(got, "# Co-authored-by") {
		t.Errorf("trailer should not be a comment:\n%s", got)
	}
}

// A missing connected set credits no one and leaves the message unchanged.
func TestCommitPrepareNoConnected(t *testing.T) {
	setupCommitEnv(t)
	msg := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(msg, []byte("subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := commitMain([]string{"prepare", msg}); code != 0 {
		t.Fatalf("prepare exit = %d, want 0", code)
	}
	if got := readFile(t, msg); got != "subject\n" {
		t.Fatalf("message changed to %q", got)
	}
}

// record learns a filled-in co-author's git name and email by position, and
// leaves an untouched placeholder unknown, so the next prepare reuses the email.
func TestCommitPrepareRecordRoundTrip(t *testing.T) {
	connected, coauthors := setupCommitEnv(t)
	if err := os.WriteFile(connected, []byte("alice\nbob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(msg, []byte("subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// First commit: both unknown; the user fills alice's line only.
	if code := commitMain([]string{"prepare", msg}); code != 0 {
		t.Fatalf("prepare exit = %d", code)
	}
	body := strings.Replace(readFile(t, msg), "Co-authored-by: alice <add your email>", "Co-authored-by: Alice Smith <alice@x.io>", 1)
	if err := os.WriteFile(msg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := commitMain([]string{"record", msg}); code != 0 {
		t.Fatalf("record exit = %d", code)
	}
	store, err := coauthor.Load(coauthors)
	if err != nil {
		t.Fatal(err)
	}
	order := store.Order([]string{"alice", "bob"})
	rendered := store.Render(order, "next\n")
	if !strings.Contains(rendered, "Co-authored-by: Alice Smith <alice@x.io>") {
		t.Errorf("alice not remembered:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Co-authored-by: bob <add your email>") {
		t.Errorf("bob should still be a placeholder:\n%s", rendered)
	}
}

// Unknown subcommands and missing arguments report a usage error.
func TestCommitMainUsage(t *testing.T) {
	if code := commitMain([]string{"prepare"}); code != 2 {
		t.Errorf("missing arg exit = %d, want 2", code)
	}
	if code := commitMain([]string{"bogus", "file"}); code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
