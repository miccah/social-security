// Package vm manages the NixOS sandbox VM: it builds and boots a composed VM (a
// base from sssh's own flake, the owner's editor/shell/tmux borrowed from the
// host, and the project's flake toolchain entered at session start), waits for
// sshd on the host-only NIC, mounts the project directory read/write, snapshots
// the disk, and GCs snapshots older than 7 days.
package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/miccah/social-security/internal/lifecycle"
)

// sandboxFlakeEnv overrides the flake reference the sandbox is built from.
const sandboxFlakeEnv = "SSSH_SANDBOX_FLAKE"

// defaultSandboxFlake is the flake reference used when sandboxFlakeEnv is unset.
// It points at sssh's own flake. The base sandbox comes from here; the owner
// environment is borrowed from the host and the project toolchain is entered at
// session start, so the flake reference stays independent of the project.
const defaultSandboxFlake = "/home/ss/ss"

// vmUser is the guest account clients log in as. The sandbox grants full access
// inside, so a single shared login is enough.
const vmUser = "root"

// bootTimeout bounds how long Start waits for the guest sshd to answer.
const bootTimeout = 2 * time.Minute

// Manager builds, boots, and tears down the sandbox VM.
type Manager interface {
	lifecycle.Manager
}

// manager boots one sandbox VM under QEMU and holds the handle the rest of sssh
// uses to reach it.
type manager struct {
	projectDir string

	// runtimeDir holds per-session state: the qcow2 disk, the SSH keypair, the
	// 9p key share, and the console log. Removed on Stop.
	runtimeDir string
	keyPath    string
	port       int
	proc       *os.Process
	// exited is closed when the QEMU process ends, so the boot wait can fail
	// fast instead of polling a dead VM.
	exited chan struct{}
}

// New returns a VM manager for the given project directory.
func New(projectDir string) Manager {
	return &manager{projectDir: projectDir}
}

func (m *manager) Name() string { return "vm" }

// Start builds the sandbox, boots it under QEMU, and returns once the guest
// sshd accepts a connection. It bounds the boot with bootTimeout and cleans up a
// half-started VM on any failure.
func (m *manager) Start(ctx context.Context) error {
	slog.Info("vm: booting sandbox", "project", m.projectDir)

	runScript, err := buildRunScript(ctx)
	if err != nil {
		return fmt.Errorf("build sandbox vm: %w", err)
	}

	m.runtimeDir, err = os.MkdirTemp("", "sssh-vm-")
	if err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			m.teardown()
		}
	}()

	if err := m.generateKey(ctx); err != nil {
		return err
	}

	m.port, err = freeLoopbackPort()
	if err != nil {
		return fmt.Errorf("reserve ssh port: %w", err)
	}

	if err := m.launch(runScript); err != nil {
		return err
	}

	if err := m.waitForSSH(ctx); err != nil {
		return err
	}

	slog.Info("vm: sandbox ready", "port", m.port, "user", vmUser, "key", m.keyPath)
	slog.Info("vm: connect",
		"cmd", fmt.Sprintf("ssh -i %s -p %d -o StrictHostKeyChecking=no %s@127.0.0.1", m.keyPath, m.port, vmUser))
	ok = true
	return nil
}

// Stop terminates the VM and removes its runtime state.
func (m *manager) Stop(context.Context) error {
	m.teardown()
	return nil
}

// buildRunScript realizes the sandbox from sssh's own flake and returns the path
// to its QEMU run script. The build is impure: the owner-environment layer reads
// the host's <nixpkgs> and /etc/nixos to borrow the owner's editor, shell, and
// tmux (see flake.nix). nix caches the derivation, so repeat calls are cheap.
func buildRunScript(ctx context.Context) (string, error) {
	flake := os.Getenv(sandboxFlakeEnv)
	if flake == "" {
		flake = defaultSandboxFlake
	}
	ref := flake + "#nixosConfigurations.sandbox.config.system.build.vm"
	out, err := exec.CommandContext(ctx, "nix", "build", ref, "--impure", "--no-link", "--print-out-paths").Output()
	if err != nil {
		return "", fmt.Errorf("nix build %s: %w", ref, cmdErr(err))
	}
	outPath := strings.TrimSpace(string(out))
	if outPath == "" {
		return "", fmt.Errorf("nix build %s produced no output path", ref)
	}
	return filepath.Join(outPath, "bin", "run-sandbox-vm"), nil
}

// freeLoopbackPort reserves an ephemeral loopback port by opening and closing a
// listener. QEMU forwards this host port into the guest sshd, so ssh access
// stays confined to loopback.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// generateKey writes an ed25519 keypair into the runtime dir and stages the
// public half in the 9p share the guest installs as its authorized key.
func (m *manager) generateKey(ctx context.Context) error {
	m.keyPath = filepath.Join(m.runtimeDir, "id_ed25519")
	gen := exec.CommandContext(ctx, "ssh-keygen", "-t", "ed25519", "-N", "", "-C", "sssh-sandbox", "-f", m.keyPath)
	if out, err := gen.CombinedOutput(); err != nil {
		return fmt.Errorf("generate ssh key: %v: %s", err, strings.TrimSpace(string(out)))
	}

	shareDir := filepath.Join(m.runtimeDir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		return fmt.Errorf("create key share: %w", err)
	}
	pub, err := os.ReadFile(m.keyPath + ".pub")
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "authorized_keys"), pub, 0o644); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}
	return nil
}

// launch starts the QEMU run script. The script boots the guest, forwards
// loopback:port to the guest sshd, shares the key directory into the guest at
// /tmp/shared via SHARED_DIR, and shares the project read/write over 9p (mount
// tag "project") so edits land on the host and the pairing session can enter its
// flake dev environment. The process runs until Stop, so it is not tied to the
// boot context.
func (m *manager) launch(runScript string) error {
	disk := filepath.Join(m.runtimeDir, "sandbox.qcow2")
	shareDir := filepath.Join(m.runtimeDir, "share")

	console, err := os.Create(filepath.Join(m.runtimeDir, "console.log"))
	if err != nil {
		return fmt.Errorf("create console log: %w", err)
	}
	defer console.Close() // QEMU keeps its own dup of the fd after Start.

	cmd := exec.Command(runScript)
	cmd.Dir = m.runtimeDir
	cmd.Env = append(os.Environ(),
		"NIX_DISK_IMAGE="+disk,
		fmt.Sprintf("QEMU_NET_OPTS=hostfwd=tcp:127.0.0.1:%d-:22", m.port),
		"SHARED_DIR="+shareDir,
		fmt.Sprintf("QEMU_OPTS=-virtfs local,path=%s,mount_tag=project,security_model=none", m.projectDir),
	)
	cmd.Stdout = console
	cmd.Stderr = console
	// Own process group so Stop can signal QEMU and any helpers together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start qemu: %w", err)
	}

	m.proc = cmd.Process
	m.exited = make(chan struct{})
	go func() {
		cmd.Wait()
		close(m.exited)
	}()
	return nil
}

// waitForSSH blocks until the guest sshd greets with an SSH banner, or until ctx
// or bootTimeout elapses, or QEMU exits first.
func (m *manager) waitForSSH(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()

	addr := fmt.Sprintf("127.0.0.1:%d", m.port)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if sshBanner(addr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox sshd not ready within %s: %w", bootTimeout, ctx.Err())
		case <-m.exited:
			return fmt.Errorf("qemu exited before sshd was ready; see %s", filepath.Join(m.runtimeDir, "console.log"))
		case <-ticker.C:
		}
	}
}

// sshBanner reports whether a TCP dial to addr is answered with an SSH banner.
// The loopback forward accepts connections before sshd is up, so the banner, not
// a bare dial, is what marks readiness.
func sshBanner(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "SSH-")
}

// teardown terminates the QEMU process group and removes the runtime dir. It is
// safe to call when Start failed partway.
func (m *manager) teardown() {
	if m.proc != nil {
		pgid := m.proc.Pid
		// QEMU shuts the guest down on SIGTERM; escalate if it lingers.
		syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-m.exited:
		case <-time.After(10 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
		m.proc = nil
	}
	if m.runtimeDir != "" {
		os.RemoveAll(m.runtimeDir)
		m.runtimeDir = ""
	}
}

// cmdErr enriches an exec error with captured stderr when available.
func cmdErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
