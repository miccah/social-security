// Package coauthor is the commit helper: it generates and parses the git commit
// template for a pairing session based on the currently connected users, and
// owns the ordered mapping from SSH username to git name and email.
//
// Two halves cooperate. A Writer runs on the host, watching the session registry
// and materializing the connected usernames into the VM's 9p share. Inside the
// VM the sssh binary runs as git's prepare-commit-msg and commit-msg hooks
// (see cmd/sssh): "prepare" rewrites the message's Co-authored-by block to credit
// the connected users, and "record" learns each user's git name and email from a
// finished commit so later commits reuse them.
//
// The store keeps users in the order they were first added to a message. That
// order is the link between an SSH user and their line: a user edits the name and
// email in place on their first commit, so their line is matched back by its
// position, not by its text. Only the pure logic lives here; the hook wiring is
// in cmd/sssh and the git config in sandbox/git.nix.
package coauthor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// trailerKey is the git trailer that records a co-author.
const trailerKey = "Co-authored-by"

// unknownEmail fills a co-author line whose email is not yet known. It carries no
// "@", so it is never mistaken for a real address and never recorded, and it
// prompts the user to complete it on their first commit.
const unknownEmail = "add your email"

// coauthorLine matches a Co-authored-by trailer, capturing the name and the
// address inside the angle brackets. Callers skip comment lines first.
var coauthorLine = regexp.MustCompile(`(?i)^` + trailerKey + `:\s*(.+?)\s*<([^>]*)>\s*$`)

// entry is one co-author's remembered git identity.
type entry struct {
	ssh   string // SSH username, the stable key
	name  string // git author name, defaulting to the SSH username
	email string // git email, empty until learned from a commit
}

// Store is the ordered co-author memory, keyed by SSH username. Order is the
// sequence in which users were first credited, so a user's line keeps its
// position across commits.
type Store struct {
	entries []entry
}

// Load reads the store at path. Each line is "ssh\tname\temail" (email may be
// empty). A missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := &Store{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		e := entry{ssh: parts[0]}
		if len(parts) > 1 {
			e.name = parts[1]
		}
		if len(parts) > 2 {
			e.email = parts[2]
		}
		if e.name == "" {
			e.name = e.ssh
		}
		s.entries = append(s.entries, e)
	}
	return s, sc.Err()
}

// Save writes the store to path, creating parent directories. Order is preserved.
func (s *Store) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range s.entries {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.ssh, e.name, e.email)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Order returns the SSH usernames to credit: every connected user in remembered
// order, with newly connected users appended in the order given. New users are
// added to the store, so prepare and record (and a later Load) agree on the
// order that maps each line to a user.
func (s *Store) Order(connected []string) []string {
	live := make(map[string]bool, len(connected))
	for _, u := range connected {
		live[u] = true
	}
	seen := make(map[string]bool, len(s.entries))
	for _, e := range s.entries {
		seen[e.ssh] = true
	}
	for _, u := range connected {
		if u != "" && !seen[u] {
			s.entries = append(s.entries, entry{ssh: u, name: u})
			seen[u] = true
		}
	}
	var out []string
	for _, e := range s.entries {
		if live[e.ssh] {
			out = append(out, e.ssh)
		}
	}
	return out
}

// get returns the entry for an SSH username, or nil.
func (s *Store) get(ssh string) *entry {
	for i := range s.entries {
		if s.entries[i].ssh == ssh {
			return &s.entries[i]
		}
	}
	return nil
}

// Render rewrites the Co-authored-by block of msg to credit the users in order,
// using each one's known identity and a placeholder for an unknown email. It
// places the block at the end of the user's message, above the comment and diff
// block git appends, and replaces any existing co-author trailers, so it is
// idempotent across re-runs and tracks users connecting and disconnecting.
func (s *Store) Render(order []string, msg string) string {
	var lines []string
	for _, ssh := range order {
		e := s.get(ssh)
		if e == nil {
			continue
		}
		email := e.email
		if email == "" {
			email = unknownEmail
		}
		lines = append(lines, fmt.Sprintf("%s: %s <%s>", trailerKey, e.name, email))
	}
	body, tail := splitTemplate(msg)
	body = stripCoauthors(body)
	if len(lines) == 0 {
		return body + tail
	}
	return strings.TrimRight(body, "\n") + "\n\n" + strings.Join(lines, "\n") + "\n" + tail
}

// splitTemplate separates the user's message from the trailing block git appends:
// the comment lines, and with `commit -v` the scissors line and the diff below
// it. The boundary is the first comment line; everything from there down is git's,
// so co-author trailers belong just above it and the diff below is left untouched.
// It assumes the default comment character.
func splitTemplate(msg string) (body, tail string) {
	lines := strings.SplitAfter(msg, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			return strings.Join(lines[:i], ""), strings.Join(lines[i:], "")
		}
	}
	return msg, ""
}

// Learn updates the store from a finished commit message, mapping each
// Co-authored-by line to a credited SSH user by position. A name edit is always
// recorded; an email is recorded only when it is a plausible address, so an
// untouched placeholder leaves the user unknown.
func (s *Store) Learn(order []string, msg string) {
	body, _ := splitTemplate(msg)
	lines := parseCoauthors(body)
	for i, ssh := range order {
		if i >= len(lines) {
			break
		}
		e := s.get(ssh)
		if e == nil {
			continue
		}
		if lines[i].name != "" {
			e.name = lines[i].name
		}
		if validEmail(lines[i].email) {
			e.email = lines[i].email
		}
	}
}

// parsed is a name and email read from one Co-authored-by line.
type parsed struct {
	name  string
	email string
}

// parseCoauthors returns the real Co-authored-by lines of msg in order, skipping
// comments.
func parseCoauthors(msg string) []parsed {
	var out []parsed
	sc := bufio.NewScanner(strings.NewReader(msg))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := coauthorLine.FindStringSubmatch(line); m != nil {
			out = append(out, parsed{name: m[1], email: m[2]})
		}
	}
	return out
}

// stripCoauthors removes the real Co-authored-by lines from msg, leaving comments
// and body intact so Render can rewrite the block from scratch.
func stripCoauthors(msg string) string {
	var kept []string
	sc := bufio.NewScanner(strings.NewReader(msg))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") && coauthorLine.MatchString(trimmed) {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	if strings.HasSuffix(msg, "\n") && out != "" {
		out += "\n"
	}
	return out
}

// validEmail reports whether s is a plausible git email: it holds an "@" and no
// whitespace, which rejects the placeholder and other non-addresses.
func validEmail(s string) bool {
	return strings.Contains(s, "@") && !strings.ContainsAny(s, " \t")
}

// ReadList reads a newline-separated list, trimming blanks. A missing file yields
// an empty list, so a commit before any connection is not an error.
func ReadList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}
