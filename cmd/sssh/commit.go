package main

import (
	"fmt"
	"os"

	"github.com/miccah/social-security/internal/coauthor"
)

// connectedFileEnv and coauthorsFileEnv override the helper's file locations, for
// tests. Unset, the helper uses the in-VM defaults below.
const (
	connectedFileEnv = "SSSH_CONNECTED_FILE"
	coauthorsFileEnv = "SSSH_COAUTHORS_FILE"
)

// defaultConnectedFile is where the host publishes the connected usernames into
// the VM's 9p share.
const defaultConnectedFile = "/tmp/shared/connected"

// defaultCoauthorsFile is the in-VM identity store mapping username to email. It
// lives on tmpfs and persists for the session.
const defaultCoauthorsFile = "/run/sssh/coauthors"

// commitMain is the in-VM git hook helper. `sssh commit prepare <msgfile>`
// appends a Co-authored-by trailer for each connected user; `sssh commit record
// <msgfile>` learns their emails from a finished commit. It is wired as the VM's
// prepare-commit-msg and commit-msg hooks (see sandbox/git.nix). It returns a
// process exit code.
func commitMain(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sssh commit <prepare|record> <msgfile>")
		return 2
	}
	sub, file := args[0], args[1]
	switch sub {
	case "prepare":
		return commitPrepare(file)
	case "record":
		return commitRecord(file)
	default:
		fmt.Fprintf(os.Stderr, "sssh commit: unknown subcommand %q\n", sub)
		return 2
	}
}

func connectedFile() string { return envOr(connectedFileEnv, defaultConnectedFile) }
func coauthorsFile() string { return envOr(coauthorsFileEnv, defaultCoauthorsFile) }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// commitPrepare rewrites the message's Co-authored-by block to credit the
// connected users, in remembered order. A missing connected set credits no one
// rather than failing the commit.
func commitPrepare(file string) int {
	users, store, existing, code := commitState(file)
	if code != 0 {
		return code
	}
	order := store.Order(users)
	rewritten := store.Render(order, string(existing))
	if rewritten != string(existing) {
		if err := os.WriteFile(file, []byte(rewritten), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "sssh commit: write message: %v\n", err)
			return 1
		}
	}
	if err := store.Save(coauthorsFile()); err != nil {
		fmt.Fprintf(os.Stderr, "sssh commit: save identities: %v\n", err)
		return 1
	}
	return 0
}

// commitRecord learns each co-author's git name and email from the finished
// commit message, mapping every line to a user by its position in the remembered
// order, and stores them for later commits.
func commitRecord(file string) int {
	users, store, msg, code := commitState(file)
	if code != 0 {
		return code
	}
	order := store.Order(users)
	store.Learn(order, string(msg))
	if err := store.Save(coauthorsFile()); err != nil {
		fmt.Fprintf(os.Stderr, "sssh commit: save identities: %v\n", err)
		return 1
	}
	return 0
}

// commitState loads the inputs both hooks share: the connected users, the
// identity store, and the current message. A non-zero code signals a read error
// the caller should return.
func commitState(file string) (users []string, store *coauthor.Store, msg []byte, code int) {
	users, err := coauthor.ReadList(connectedFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "sssh commit: read connected set: %v\n", err)
		return nil, nil, nil, 1
	}
	store, err = coauthor.Load(coauthorsFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "sssh commit: load identities: %v\n", err)
		return nil, nil, nil, 1
	}
	msg, err = os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sssh commit: read message: %v\n", err)
		return nil, nil, nil, 1
	}
	return users, store, msg, 0
}
