# social-security

Pair program with anyone over `ssh`, inside a throwaway sandbox, with a "social
security" trust model.

Simply run `sssh` in a project directory, and it boots a NixOS VM with that
directory mounted live, opens an ngrok tunnel, and runs an SSH front door.
Guests connect with `ssh`, pick a username, read out a "social security number"
for the owner to match out-of-band, and wait to be accepted. Accepted guests
land in a shared `tmux` session running *inside the VM*. When the owner
disconnects, everyone is dropped and the VM is destroyed.

## Purpose

I have a NixOS dev machine that I would like others to be able to pair program
on, without any setup on their part.

## How it works

```
  guest (internet) ──ngrok──┐
                            ▼
  owner (LAN) ──────► SSH front door (wish) ──► NixOS VM ──► shared tmux
                            │                    (project mounted live)
                            ▼
                     control plane (launching terminal)
                     accept / decline / kick
```

For the full design see [`docs/DESIGN.md`](docs/DESIGN.md); requirements in
[`docs/PRD.md`](docs/PRD.md); milestones in [`docs/PLAN.md`](docs/PLAN.md).

## Requirements

- **A NixOS host.** The sandbox borrows the owner's editor, shell, and tmux from
  the running host config (`/etc/nixos`, `<nixpkgs>`), so the build is impure and
  currently assumes NixOS.
- **Nix with flakes**, and **QEMU/KVM** for the VM.
- **An ngrok authtoken** for public ingress.
- **Go 1.26+** to build.

## Build

```sh
go build -o bin/sssh ./cmd/sssh
```

A dev shell with the Go toolchain is available via `nix develop`. The binary
embeds the sandbox flake (see `embed.go`), so it carries everything `nix build`
needs to realize the VM.

## Usage

In the project directory, with an ngrok token exported:

```sh
export NGROK_AUTHTOKEN=...
sssh
```

This boots the VM, opens the tunnel, and stays interactive as the control plane,
printing two connect strings:

1. **Owner**: in a second terminal, run the printed owner `ssh` command (over
   loopback/LAN). You drop straight into the shared tmux with no ceremony.
2. **Guest**: share the printed guest `ssh` command. On connect a guest is
   prompted for a username and an SSN, then waits for approval.

When a guest is waiting, the control plane shows the request. Confirm the SSN
with them out-of-band, then accept.

### Control plane keys

| Key       | Action                               |
|-----------|--------------------------------------|
| `j` / `k` | move the cursor                      |
| `a`       | accept the selected pending request  |
| `d`       | decline the selected pending request |
| `x`       | kick the selected active guest       |
| `q`       | quit and end the session for everyone|

Guests may only be accepted once the owner has joined the session. A kicked guest
may reconnect as a fresh request.

### Ending a session

Quitting the control plane (`q`) or the owner disconnecting the shared session
tears everything down: guests are dropped, the VM is destroyed, and the tunnel is
closed. Project edits are already on the host.

## Git and commits

When the project is a git repo:

- Commits are **authored by the owner**, whose git identity is seeded into the VM.
- Each connected guest is added as a `Co-authored-by` trailer. A guest completes
  their email on their first commit; later commits reuse it.
- **`git push` is disabled** for the session: no push credentials enter the VM and
  a pre-push hook rejects pushes.

Non-git projects are mounted and editable with no git setup.

## Configuration

| Variable               | Purpose                                             |
|------------------------|-----------------------------------------------------|
| `NGROK_AUTHTOKEN`      | ngrok authtoken (required).                          |
| `SSSH_FRONTDOOR_ADDR`  | Front door listen address (default `:1337`).         |
| `SSSH_HOST_KEY`        | Path to the front door's persistent SSH host key.    |
| `SSSH_SANDBOX_FLAKE`   | Build the sandbox from this flake path instead of the embedded one. |

## Security model

The trust model is **social**: the owner watches what accepted guests do and has
an established relationship with them. Inside the VM there are deliberately no
command or network limits, and the project directory is shared live with the host.

This is not a hardened security boundary. In particular:

- Accepted guests are trusted; the design does not defend against a malicious one.
- The project's `.git/` is part of the live mount, so treat the shared directory
  as guest-writable.
- The owner path is recognized by network origin, not authenticated. **Run on a
  trusted LAN**, and consider binding the front door to loopback
  (`SSSH_FRONTDOOR_ADDR=127.0.0.1:1337`) where LAN guests are not needed.

Public ingress is ngrok-only; the control plane runs as a separate host-side
process guests never connect to.

## AI disclosure

This project was largely written by AI under the guidance of a professional
software engineer. I (Miccah) wrote the [PRD](docs/PRD.md), and used AI to
generate the [DESIGN](docs/DESIGN.md) (which I reviewed), before having it
generate a [PLAN](docs/PLAN.md) (which I also reviewed). Each change was also
tested and reviewed before committing by me.
