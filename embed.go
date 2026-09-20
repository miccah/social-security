// Package sssh embeds the Nix sources that define the sandbox VM so the binary
// carries them instead of reading them from a fixed host path at runtime.
package sssh

import "embed"

// SandboxFlake holds the flake entry point, its lockfile, and the sandbox
// modules it imports: everything `nix build` needs to realize the VM.
//
//go:embed flake.nix flake.lock sandbox
var SandboxFlake embed.FS
