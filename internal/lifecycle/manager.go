// Package lifecycle defines the start/stop contract shared by sssh's long-lived
// subsystems (the VM, tunnel, front door, registry, and control plane). The
// entrypoint starts them in dependency order and stops them in reverse, so this
// one small interface is all it needs to orchestrate startup and graceful
// teardown.
package lifecycle

import "context"

// Manager is one long-lived subsystem with a start/stop lifecycle.
//
// Start must not block for the subsystem's lifetime: it brings the subsystem up
// and returns, spawning any long-running work on its own goroutine bound to the
// context. This lets the entrypoint start every manager in sequence and then
// block on a single signal, rather than one Start call stalling the whole boot.
//
// Stop must be safe to call even when Start failed partway, because teardown
// runs over whatever managed to start. It should be called with a distinct
// context from Start to allow bounded cleanup work.
type Manager interface {
	// Name identifies the manager in startup and teardown logs.
	Name() string
	// Start brings the subsystem up without blocking for its lifetime.
	Start(ctx context.Context) error
	// Stop releases the subsystem's resources.
	Stop(ctx context.Context) error
}
