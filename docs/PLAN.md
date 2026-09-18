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

**Goal:** prove the sandbox — the highest-unknown work, done first.

- [ ] The sandbox `nixosConfiguration` builds from **sssh's own flake** — prebuilt and
      pinned at build time, never evaluated from the project directory / `$PWD` — and
      includes sshd, tmux, git, and the baseline dev tools on `$PATH`.
- [ ] A project directory with no flake (plain dir or git repo) boots the identical
      sandbox.
- [ ] The VM boots via QEMU/KVM and sshd is reachable only on the host-only NIC
      (not on any public or LAN interface).
- [ ] The VM manager blocks until sshd accepts a connection, with a bounded timeout
      that returns a clear error on failure.
- [ ] A `pairing` tmux session exists in the VM; `ssh <vm> tmux attach -t pairing`
      from the host attaches to it.

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

- [ ] Startup opens an ngrok TCP endpoint and prints guest + owner connect strings.
- [ ] ngrok failure aborts startup with a clear error and exposes nothing.
- [ ] A guest using the printed string over the internet reaches the front door and
      is bridged into the shared tmux (auto-accept placeholder).
- [ ] Teardown closes the tunnel; the public address stops accepting connections.

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

## M6 — Seeding + reintegration

**Goal:** real projects go in and edits come back out.

- [ ] On startup the entire project directory is copied into the VM verbatim
      (contents match, no host mount).
- [ ] For a git repo, owner git creds are present in the VM and a commit + `git push`
      to the upstream succeeds from inside the session.
- [ ] On teardown the VM working directory is written back to a sibling `sssh-out/`
      on the host.
- [ ] An edit made inside the session is present in `sssh-out/` after teardown.
- [ ] A non-git project seeds and writes back without requiring creds.

## M7 — Lifecycle + retention hardening

**Goal:** the §10 failure table behaves as specified.

- [ ] Owner disconnect (detach, quit, or simulated network drop) disconnects all
      guests and tears down the VM, tunnel, and session.
- [ ] Guest drop removes it from `active`, frees the username, and leaves the session
      running for others.
- [ ] VM-boot failure aborts startup before any tunnel is opened.
- [ ] Teardown snapshots the VM disk; a startup GC pass deletes snapshots older than
      7 days and retains newer ones.
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
