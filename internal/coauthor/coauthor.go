// Package coauthor is the commit helper: it generates and parses the git commit
// template for a pairing session based on the currently connected users, and
// owns the mapping from SSH username to git email.
//
// Two halves cooperate. A Writer runs on the host, watching the session registry
// and materializing the connected usernames into the VM's 9p share. Inside the
// VM the sssh binary runs as git's prepare-commit-msg and commit-msg hooks
// (see cmd/sssh): "prepare" appends a Co-authored-by trailer for each connected
// user, and "record" learns a user's email from a finished commit so later
// commits reuse it. Only the pure generate/parse/store logic lives here; the
// hook wiring is in cmd/sssh and the git config in sandbox/git.nix.
package coauthor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// trailerKey is the git trailer that records a co-author.
const trailerKey = "Co-authored-by"

// emailPlaceholder fills a co-author line whose email is not yet known. It is
// emitted as a commented hint, so git strips it unless the user completes it on
// their first commit. It carries no "@", so Parse never mistakes it for a real
// address.
const emailPlaceholder = "add your email"

// coauthorLine matches a Co-authored-by trailer, capturing the name and the
// address inside the angle brackets. The caller strips comment lines first.
var coauthorLine = regexp.MustCompile(`(?i)^` + trailerKey + `:\s*(.+?)\s*<([^>]*)>\s*$`)

// Message returns the block to append to a commit message crediting the
// connected users as co-authors. A user with a known email gets a real trailer;
// a user without one gets a commented hint to complete on their first commit.
// Users already credited in existing are skipped, so re-running the hook (an
// amend, a second invocation) does not duplicate them. It returns "" when there
// is nothing to add.
func Message(users []string, known map[string]string, existing string) string {
	var lines []string
	for _, u := range users {
		if u == "" || creditsUser(existing, u) {
			continue
		}
		if email := known[u]; email != "" {
			lines = append(lines, fmt.Sprintf("%s: %s <%s>", trailerKey, u, email))
		} else {
			lines = append(lines, fmt.Sprintf("# %s: %s <%s>", trailerKey, u, emailPlaceholder))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	// A leading blank line keeps the trailers in their own final paragraph, so
	// git parses them as trailers rather than body text.
	return "\n" + strings.Join(lines, "\n") + "\n"
}

// Parse extracts username to email pairs from the real Co-authored-by trailers in
// a commit message. Commented hints and lines whose address is not a plausible
// email are ignored, so an untouched placeholder is never recorded.
func Parse(msg string) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(msg))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := coauthorLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, email := m[1], m[2]
		if !validEmail(email) {
			continue
		}
		out[name] = email
	}
	return out
}

// creditsUser reports whether existing already carries a Co-authored-by line for
// user, whether a real trailer or a commented hint.
func creditsUser(existing, user string) bool {
	sc := bufio.NewScanner(strings.NewReader(existing))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "#"))
		m := coauthorLine.FindStringSubmatch(line)
		if m != nil && m[1] == user {
			return true
		}
	}
	return false
}

// validEmail reports whether s is a plausible git email: it holds an "@" and no
// whitespace, which rejects the placeholder and other non-addresses.
func validEmail(s string) bool {
	return strings.Contains(s, "@") && !strings.ContainsAny(s, " \t")
}

// Load reads the identity store at path, a "username=email" line per entry. A
// missing file yields an empty map.
func Load(path string) (map[string]string, error) {
	out := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, email, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(email)
	}
	return out, sc.Err()
}

// Save writes the identity store to path, creating parent directories. Entries
// are sorted so the file is stable across writes.
func Save(path string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "%s=%s\n", name, m[name])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ReadList reads a newline-separated list, trimming blanks. A missing file
// yields an empty list, so a commit before any connection is not an error.
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
