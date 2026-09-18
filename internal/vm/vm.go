// Package vm manages the NixOS sandbox VM: it builds and boots the VM from
// sssh's own flake, waits for sshd on the host-only NIC, seeds the project
// directory, writes it back on teardown, snapshots the disk, and GCs snapshots
// older than 7 days.
package vm

import (
	"context"
	"log/slog"

	"github.com/miccah/social-security/internal/lifecycle"
)

// Manager builds, boots, and tears down the sandbox VM.
type Manager interface {
	lifecycle.Manager
}

// stub is the no-op implementation. It already carries the project directory so
// the seeding step has the absolute path it will copy into the VM.
type stub struct {
	projectDir string
}

// New returns a no-op VM manager for the given project directory (the absolute
// path resolved by the entrypoint).
func New(projectDir string) Manager {
	return &stub{projectDir: projectDir}
}

func (s *stub) Name() string { return "vm" }

// Start will build from sssh's own flake, boot via QEMU/KVM, and wait for sshd
// on the host-only interface before returning.
func (s *stub) Start(context.Context) error {
	slog.Debug("vm: boot skipped (stub)", "project", s.projectDir)
	return nil
}

func (s *stub) Stop(context.Context) error { return nil }
