package coauthor

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Message emits a real trailer for a known email and a commented hint for an
// unknown one, and returns a trailing block that starts its own paragraph.
func TestMessageKnownAndUnknown(t *testing.T) {
	got := Message([]string{"alice", "bob"}, map[string]string{"alice": "alice@x.io"}, "subject\n")
	want := "\nCo-authored-by: alice <alice@x.io>\n# Co-authored-by: bob <add your email>\n"
	if got != want {
		t.Fatalf("Message =\n%q\nwant\n%q", got, want)
	}
}

// Message skips users already credited in the message, whether by a real trailer
// or a commented hint, so re-running the hook does not duplicate them.
func TestMessageSkipsExisting(t *testing.T) {
	existing := "subject\n\nCo-authored-by: alice <alice@x.io>\n# Co-authored-by: bob <add your email>\n"
	if got := Message([]string{"alice", "bob"}, map[string]string{"alice": "alice@x.io"}, existing); got != "" {
		t.Fatalf("Message = %q, want empty", got)
	}
}

// Message returns empty when there are no connected users.
func TestMessageNoUsers(t *testing.T) {
	if got := Message(nil, nil, "subject\n"); got != "" {
		t.Fatalf("Message = %q, want empty", got)
	}
}

// Parse recovers real co-authors and ignores commented hints and untouched
// placeholders.
func TestParse(t *testing.T) {
	msg := strings.Join([]string{
		"subject",
		"",
		"Co-authored-by: alice <alice@x.io>",
		"# Co-authored-by: carol <carol@x.io>",
		"Co-authored-by: bob <add your email>",
		"Co-authored-by: dave <dave@x.io>",
	}, "\n")
	got := Parse(msg)
	want := map[string]string{"alice": "alice@x.io", "dave": "dave@x.io"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
}

// A generated hint round-trips: once the user fills the email, Parse recovers it.
func TestMessageParseRoundTrip(t *testing.T) {
	block := Message([]string{"bob"}, nil, "subject\n")
	filled := strings.Replace(block, "# Co-authored-by: bob <add your email>", "Co-authored-by: bob <bob@x.io>", 1)
	if got := Parse("subject\n" + filled); got["bob"] != "bob@x.io" {
		t.Fatalf("round-trip email = %q, want bob@x.io", got["bob"])
	}
}

// Load and Save round-trip the identity store; a missing file loads empty.
func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "coauthors")
	if m, err := Load(path); err != nil || len(m) != 0 {
		t.Fatalf("Load(missing) = %v, %v; want empty, nil", m, err)
	}
	want := map[string]string{"alice": "alice@x.io", "bob": "bob@x.io"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %v, want %v", got, want)
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
