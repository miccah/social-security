// Package control is the owner's control plane: an interactive Bubble Tea UI in
// the launching terminal. It prints the connect string, shows connected/waiting
// counts and the pending request list, and handles accept/decline/kick. It runs
// as a separate host-side process that guests never connect to, so it is
// invisible to and unreachable by them; it is never rendered into the VM tmux.
package control

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/miccah/social-security/internal/lifecycle"
	"github.com/miccah/social-security/internal/registry"
)

// Plane is the owner's control surface in the launching terminal.
type Plane interface {
	lifecycle.Manager
}

// addrProvider supplies the front door's LAN connect address (the owner).
type addrProvider interface {
	Addr() string
}

// urlProvider supplies the tunnel's public endpoint URL (guests).
type urlProvider interface {
	URL() *url.URL
}

type plane struct {
	reg    registry.Registry
	front  addrProvider
	tunnel urlProvider
	quit   func() // ends the session when the owner leaves the control plane

	prog       *tea.Program
	done       chan struct{}
	logFile    *os.File
	prevLogger *slog.Logger
}

// New returns a control plane bound to the registry, reading the owner connect
// address from front and the guest connect URL from tunnel, and calling quit
// when the owner exits the UI.
func New(reg registry.Registry, front addrProvider, tunnel urlProvider, quit func()) Plane {
	return &plane{reg: reg, front: front, tunnel: tunnel, quit: quit}
}

func (p *plane) Name() string { return "control" }

// Start routes logs to a file so the TUI owns the terminal, then runs the Bubble
// Tea program on a goroutine. Leaving the UI (or a fatal error) calls quit,
// which ends the session.
func (p *plane) Start(context.Context) error {
	f, err := os.CreateTemp("", "sssh-control-*.log")
	if err != nil {
		return fmt.Errorf("create control log: %w", err)
	}
	p.logFile = f
	fmt.Fprintf(os.Stderr, "control plane logs: %s\n", f.Name())
	p.prevLogger = slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))

	p.prog = tea.NewProgram(newModel(p.reg, guestConnectLine(p.tunnel.URL()), connectLine(p.front.Addr())), tea.WithAltScreen())
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if _, err := p.prog.Run(); err != nil {
			slog.Error("control: ui exited with error", "err", err)
		}
		p.quit()
	}()
	return nil
}

// Stop quits the UI, waits for the terminal to be restored, and puts logging
// back on its original destination.
func (p *plane) Stop(context.Context) error {
	if p.prog != nil {
		p.prog.Quit()
		<-p.done
	}
	if p.prevLogger != nil {
		slog.SetDefault(p.prevLogger)
	}
	if p.logFile != nil {
		p.logFile.Close()
	}
	return nil
}

// connectLine renders the ssh command the owner runs over LAN, given the front
// door's listen address.
func connectLine(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("ssh -p %s %s", port, host)
}

// guestConnectLine renders the ssh command a guest runs against the public ngrok
// endpoint. The front door accepts any user, so none is included.
func guestConnectLine(u *url.URL) string {
	if u == nil {
		return "(unavailable)"
	}
	if port := u.Port(); port != "" {
		return fmt.Sprintf("ssh -p %s %s", port, u.Hostname())
	}
	return fmt.Sprintf("ssh %s", u.Hostname())
}

// row is one selectable entry: a pending request (accept/decline) or an active
// session (kick).
type row struct {
	pending bool
	id      string
	label   string
}

type refreshMsg struct{}

type model struct {
	reg          registry.Registry
	guestConnect string
	ownerConnect string

	rows         []row
	cursor       int
	prevWaiting  int
	ownerPresent bool
	startTime    time.Time

	confirmingQuit bool
}

func newModel(reg registry.Registry, guestConnect, ownerConnect string) model {
	m := model{reg: reg, guestConnect: guestConnect, ownerConnect: ownerConnect, startTime: time.Now()}
	m.refresh()
	return m
}

func (m model) Init() tea.Cmd { return waitForEvent(m.reg) }

// waitForEvent blocks until the registry reports a change, then asks the model to
// refresh. Coalesced events mean one wakeup reflects the latest state.
func waitForEvent(reg registry.Registry) tea.Cmd {
	return func() tea.Msg {
		<-reg.Events()
		return refreshMsg{}
	}
}

// ringBell rings the terminal bell without disturbing the alt-screen render.
func ringBell() tea.Msg {
	fmt.Fprint(os.Stderr, "\a")
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case refreshMsg:
		cmds := []tea.Cmd{waitForEvent(m.reg)}
		if m.refresh() {
			cmds = append(cmds, ringBell)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmingQuit {
		return m.handleQuitConfirm(msg)
	}
	switch msg.String() {
	case "q":
		// Quitting ends the session for everyone, so confirm first (if there are
		// any users).
		if m.reg.Count() == 0 {
			return m, tea.Quit
		}
		m.confirmingQuit = true
		return m, nil
	case "ctrl+c":
		// Ctrl+C is an immediate break; do not confirm.
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "a":
		// Guests may only be approved once the owner is in the session; the
		// registry enforces that in Resolve. This pre-gate reads live owner state
		// (not the cached snapshot) so the UI never offers an accept the registry
		// would refuse. Declining is always allowed.
		if m.reg.OwnerPresent() {
			m.resolveSelected(registry.Accept)
		}
	case "d":
		m.resolveSelected(registry.Decline)
	case "x":
		m.kickSelected()
	}
	m.refresh()
	return m, nil
}

// handleQuitConfirm resolves the quit confirmation: y quits, anything else
// returns to the control plane.
func (m model) handleQuitConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return m, tea.Quit
	default:
		m.confirmingQuit = false
		return m, nil
	}
}

func (m model) selected() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

func (m model) resolveSelected(d registry.Decision) {
	if r, ok := m.selected(); ok && r.pending {
		if err := m.reg.Resolve(r.id, d); err != nil {
			slog.Debug("control: resolve failed", "id", r.id, "err", err)
		}
	}
}

func (m model) kickSelected() {
	if r, ok := m.selected(); ok && !r.pending {
		m.reg.Kick(r.id)
	}
}

// refresh rebuilds the row list from the registry and reports whether the number
// of waiting requests grew, so the caller can ring the bell.
func (m *model) refresh() (grewWaiting bool) {
	m.ownerPresent = m.reg.OwnerPresent()
	pending := m.reg.Pending()
	active := m.reg.Active()

	rows := make([]row, 0, len(pending)+len(active))
	for _, req := range pending {
		rows = append(rows, row{
			pending: true,
			id:      req.ID,
			label:   fmt.Sprintf("%s  SSN %s  from %s", req.Username, req.SSN, req.RemoteAddr),
		})
	}
	for _, s := range active {
		rows = append(rows, row{
			pending: false,
			id:      s.ID,
			label:   fmt.Sprintf("%s  from %s", s.Username, s.RemoteAddr),
		})
	}
	m.rows = rows
	if m.cursor >= len(rows) {
		m.cursor = max(0, len(rows)-1)
	}

	grewWaiting = len(pending) > m.prevWaiting
	m.prevWaiting = len(pending)
	return grewWaiting
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString("sssh control\n\n")
	fmt.Fprintf(&b, "%-15s %s\n", "guest connect:", m.guestConnect)
	fmt.Fprintf(&b, "%-15s %s\n", "owner connect:", m.ownerConnect)

	ownerStatus := "awaiting connection"
	if m.ownerPresent {
		ownerStatus = "joined"
	}
	fmt.Fprintf(&b, "%-15s %s\n", "owner:", ownerStatus)

	duration := time.Since(m.startTime)
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60
	fmt.Fprintf(&b, "%-15s %02d:%02d:%02d\n\n", "uptime:", hours, minutes, seconds)

	waiting := 0
	for _, r := range m.rows {
		if r.pending {
			waiting++
		}
	}
	fmt.Fprintf(&b, "connected: %d   waiting: %d\n\n", len(m.rows)-waiting, waiting)

	if len(m.rows) == 0 {
		b.WriteString("  (no one connected)\n")
	}
	for i, r := range m.rows {
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		tag := "active "
		if r.pending {
			tag = "pending"
		}
		fmt.Fprintf(&b, "%s%s  %s\n", cursor, tag, r.label)
	}

	if m.confirmingQuit {
		b.WriteString("\nquit and end the session for everyone? [y/N]\n")
	} else {
		if !m.ownerPresent {
			b.WriteString("\njoin the session (owner connect) to approve guests\n")
		}
		b.WriteString("\nj/k move  [a]ccept  [d]ecline  [x]kick  [q]uit\n")
	}
	return b.String()
}
