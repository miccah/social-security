# social-shecurity — Implementation Plan

Milestone breakdown for the design in [`DESIGN.md`](./DESIGN.md) (against the
requirements in [`PRD.md`](./PRD.md)).

Ordered to reach a thin end-to-end slice early and to front-load the riskiest piece
(the sandbox VM), then layer on ingress, control, and safety. M0–M2 is the first
demoable vertical (local pairing); M3–M5 makes it public and owner-controlled;
M6–M8 make it safe and complete.

Each milestone lists acceptance criteria — the conditions that must hold for it to
be considered done. A milestone is complete only when every box is checked.

---

## M0 — Skeleton

**Goal:** a binary that starts and stops cleanly.

- [ ] `go build ./...` produces the `sssh` binary with no errors.
- [ ] Run in a directory resolves and logs the absolute project path.
- [ ] VM, tunnel, front-door, and registry managers exist as interfaces with no-op
      implementations wired through the top-level context.
- [ ] SIGINT/SIGTERM triggers ordered teardown of every registered manager; the
      process exits 0 with no leaked goroutines.

## M1 — Sandbox VM

**Goal:** prove the sandbox — the highest-unknown work, done first. The sandbox is
composed (base + owner environment + project toolchain), not a fixed image (DESIGN §9).

**Base**
- [ ] The sandbox `nixosConfiguration` builds from **sssh's own flake** and provides the
      base layer — sshd (key-only), tmux, git, the `pairing` session — whose security
      settings override the layers below where they collide.
- [ ] The VM boots via QEMU/KVM and sshd is reachable only on the host-only NIC
      (not on any public or LAN interface).
- [ ] The VM manager blocks until sshd accepts a connection, with a bounded timeout
      that returns a clear error on failure.
- [ ] A `pairing` tmux session exists in the VM; `ssh <vm> tmux attach -t pairing`
      from the host attaches to it.

**Owner environment**
- [ ] The sandbox imports the owner's host program modules (editor, shell, tmux) and
      only those — never the host's bootloader, hardware, users, or secrets.
- [ ] The shared session presents the owner's real editor/shell/tmux configuration,
      not a generic default.

**Project toolchain**
- [ ] A project with a flake `devShell` boots its panes inside that shell (`nix develop`
      / a `use flake` `.envrc`); its compilers and LSPs are on `$PATH`.
- [ ] The host Nix store is shared into the VM, so an already-built dev environment
      resolves without a rebuild; only uncached inputs build at session start.
- [ ] A project with no flake falls back to the base tools and still boots.

## M2 — Local shared session

**Goal:** the core pairing loop, on the LAN, no internet yet.

- [ ] The front door listens on a loopback/LAN address that is logged at startup.
- [ ] A stock `ssh` client connecting over LAN lands in the VM `pairing` tmux, never
      a host shell.
- [ ] Two simultaneous connections see mirrored, read/write tmux content.
- [ ] Client window resize propagates to the tmux pane.
- [ ] The registry tracks owner presence and the active set; disconnect removes the
      entry.

## M3 — Public ingress

**Goal:** guests can reach the front door from the internet.

- [x] Startup opens an ngrok TCP endpoint and prints guest + owner connect strings.
- [x] ngrok failure aborts startup with a clear error and exposes nothing.
- [x] A guest using the printed string over the internet reaches the front door and
      is bridged into the shared tmux (auto-accept placeholder).
- [x] Teardown closes the tunnel; the public address stops accepting connections.

## M4 — Join ceremony + registry

**Goal:** guests queue for approval instead of auto-joining.

- [ ] On connect a guest is prompted for a username, then an SSN.
- [ ] A username already in `active`/`pending` is rejected and the guest is reprompted.
- [ ] The request lands in `pending` with username, SSN, remote address, and a
      buffered decision channel.
- [ ] The handler blocks until `Resolve(id, …)` or ctx cancel: accept bridges into
      tmux, decline disconnects with a message.
- [ ] A guest disconnecting or timing out while pending drops the request and frees
      the username.

## M5 — Control plane

**Goal:** the owner runs the whole session from the launching terminal.

- [ ] The launching terminal renders the control plane with connect strings and live
      connected/waiting counts.
- [ ] A new pending request appears ambiently (no focus steal) with a soft bell;
      Terminal B (the editor) is unaffected.
- [ ] `a` / `d` / `k` accept, decline, and kick the selected user and reflect in the
      registry within the session.
- [ ] Kick closes the target's bridge and PTY and frees the username; the guest can
      reconnect as a fresh pending request.
- [ ] Guests never see the control plane — it is never rendered into the VM tmux.

## M6 — Project mount + git

**Goal:** the live project is in the VM; commits work from inside, push is disabled.

- [x] The host project directory is mounted read/write into the VM; an edit made in
      the session appears on the host live (no copy-in, no write-back).
- [x] For a git repo, the VM's git config carries the owner's identity, so a commit
      made in the session is authored by the owner.
- [x] The commit helper generates the template from the currently connected users,
      emitting a `Co-authored-by: <username> <email>` line for each.
- [x] The commit helper parses a completed commit to capture a user's email on their
      first commit (a session with no commits never asks) and maps it to their SSH
      username.
- [x] A captured email is reused by later commits without reprompting; the template
      updates as users connect and disconnect.
- [x] `git push` from inside the session fails for everyone (no push credentials, and a
      pre-push hook rejects it).
- [x] A non-git project mounts and is editable without requiring git setup.

## M7 — Lifecycle hardening

**Goal:** the §10 failure table behaves as specified.

- [ ] Owner disconnect (detach, quit, or simulated network drop) disconnects all
      guests and tears down the VM, tunnel, and session.
- [ ] Guest drop removes it from `active`, frees the username, and leaves the session
      running for others.
- [ ] VM-boot failure aborts startup before any tunnel is opened.
- [ ] Teardown destroys the VM; nothing is retained (the project lives on the host).
- [ ] Every row of DESIGN §10 has a corresponding passing test or manual verification.

## M8 — Polish (open questions)

**Goal:** resolve the §11 open questions that remain.

- [ ] The front door presents a persistent SSH host key across sessions (no host-key
      warning churn for repeat guests).
- [ ] A configured maximum guest count is enforced; the next guest is refused with a
      message.
- [ ] A pending request expires after a configurable timeout and notifies the guest.
- [ ] A project exceeding the size cap is refused (or explicitly opted past) with a
      clear message.
- [ ] Each resolved open question in §11 is reflected back into `DESIGN.md`.

---

## Side goals (non-blocking)

These improve the design but are not on the M0–M8 critical path; the milestones above
work without them.

### S1 — Shared host environment module

Factor the environment layer of `/etc/nixos` (`programs.neovim`, `programs.tmux`,
`programs.zsh`, the home-manager user module) into a standalone module imported by both
the host system and the sandbox (DESIGN §9). M1 imports the host paths directly in the
meantime; this removes the path coupling and the two-nixpkgs concern (DESIGN §11.12).

- [ ] The editor/shell/tmux configuration lives in one module imported by both
      `/etc/nixos` and the sandbox.
- [ ] The sandbox no longer references `/etc/nixos` paths directly.
- [ ] Host and sandbox evaluate that environment against a single nixpkgs.
