package control

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/miccah/social-security/internal/registry"
)

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// regWithActive returns a registry holding one active session, so Count() > 0.
func regWithActive(t *testing.T) registry.Registry {
	t.Helper()
	reg := registry.New()
	reg.ClaimOwner()
	req, err := reg.AddPending("alice", "1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Resolve(req.ID, registry.Accept); err != nil {
		t.Fatal(err)
	}
	<-req.Decision()
	reg.Activate(req, func() {})
	return reg
}

// Pressing 'a' accepts the selected pending request once the owner has joined.
func TestAcceptResolvesSelectedPending(t *testing.T) {
	reg := registry.New()
	reg.ClaimOwner()
	req, err := reg.AddPending("alice", "111", "10.0.0.1:2200")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "ssh -p 1 tcp.ngrok.io", "ssh -p 1337 host")
	m.Update(key('a'))

	if len(reg.Pending()) != 0 {
		t.Fatal("accept should remove the request from pending")
	}
	select {
	case d := <-req.Decision():
		if d != registry.Accept {
			t.Fatalf("decision = %v, want Accept", d)
		}
	default:
		t.Fatal("no decision delivered to the handler")
	}
}

// Accept is inert until the owner is in the session; the request stays pending.
func TestAcceptBlockedUntilOwnerPresent(t *testing.T) {
	reg := registry.New()
	req, err := reg.AddPending("alice", "111", "10.0.0.1:2200")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "g", "x")
	m.Update(key('a'))

	if len(reg.Pending()) != 1 {
		t.Fatal("accept should be blocked while the owner is absent")
	}
	select {
	case <-req.Decision():
		t.Fatal("no decision should be delivered while the owner is absent")
	default:
	}

	// Once the owner joins, the same keystroke accepts.
	reg.ClaimOwner()
	m.Update(key('a'))
	if len(reg.Pending()) != 0 {
		t.Fatal("accept should resolve the request once the owner is present")
	}
	if d := <-req.Decision(); d != registry.Accept {
		t.Fatalf("decision = %v, want Accept", d)
	}
}

// Declining is allowed even before the owner joins, and frees the username.
func TestDeclineAllowedWithoutOwner(t *testing.T) {
	reg := registry.New()
	req, err := reg.AddPending("bob", "1", "a")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "g", "x")
	m.Update(key('d'))

	if d := <-req.Decision(); d != registry.Decline {
		t.Fatalf("decision = %v, want Decline", d)
	}
	if _, err := reg.AddPending("bob", "1", "a"); err != nil {
		t.Fatalf("username should be free after decline: %v", err)
	}
}

// Pressing 'd' declines the selected pending request and frees the username.
func TestDeclineResolvesSelectedPending(t *testing.T) {
	reg := registry.New()
	req, err := reg.AddPending("bob", "1", "a")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "g", "x")
	m.Update(key('d'))

	select {
	case d := <-req.Decision():
		if d != registry.Decline {
			t.Fatalf("decision = %v, want Decline", d)
		}
	default:
		t.Fatal("no decision delivered")
	}
	if _, err := reg.AddPending("bob", "1", "a"); err != nil {
		t.Fatalf("username should be free after decline: %v", err)
	}
}

// Pressing 'k' kicks the selected active session.
func TestKickInvokesOnKick(t *testing.T) {
	reg := registry.New()
	reg.ClaimOwner()
	req, _ := reg.AddPending("carol", "1", "a")
	reg.Resolve(req.ID, registry.Accept)
	<-req.Decision()
	kicked := make(chan struct{}, 1)
	reg.Activate(req, func() { kicked <- struct{}{} })

	m := newModel(reg, "g", "x")
	m.Update(key('x'))

	select {
	case <-kicked:
	default:
		t.Fatal("kick should invoke the session's onKick")
	}
}

// The cursor selects among rows so an action targets the intended entry.
func TestNavigateAndAcceptSecond(t *testing.T) {
	reg := registry.New()
	reg.ClaimOwner()
	first, _ := reg.AddPending("a", "1", "x")
	time.Sleep(time.Millisecond)
	second, _ := reg.AddPending("b", "2", "y")

	m := newModel(reg, "g", "x")
	next, _ := m.Update(key('j')) // vi-style down
	m = next.(model)
	m.Update(key('a'))

	pending := reg.Pending()
	if len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("expected only the first request pending, got %+v", pending)
	}
	select {
	case d := <-second.Decision():
		if d != registry.Accept {
			t.Fatalf("decision = %v, want Accept", d)
		}
	default:
		t.Fatal("second request not resolved")
	}
}

// A newly arrived request rings the bell once.
func TestBellOnNewPending(t *testing.T) {
	reg := registry.New()
	m := newModel(reg, "g", "x")

	reg.AddPending("dave", "1", "a")
	if !m.refresh() {
		t.Fatal("expected the bell when a request arrives")
	}
	if m.refresh() {
		t.Fatal("no bell when nothing new arrived")
	}
}

// With users present, quitting asks for confirmation first, then y ends it.
func TestQuitConfirmsWhenUsersPresent(t *testing.T) {
	m := newModel(regWithActive(t), "g", "x")

	next, cmd := m.Update(key('q'))
	if cmd != nil {
		t.Fatal("q should prompt, not quit, while users are present")
	}
	m = next.(model)
	if !m.confirmingQuit {
		t.Fatal("q should enter quit confirmation")
	}

	_, cmd = m.Update(key('y'))
	if cmd == nil {
		t.Fatal("y should confirm the quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("confirming should return tea.Quit")
	}
}

// Declining the confirmation returns to the control plane without quitting.
func TestQuitCanceled(t *testing.T) {
	m := newModel(regWithActive(t), "g", "x")

	next, _ := m.Update(key('q'))
	m = next.(model)
	if !m.confirmingQuit {
		t.Fatal("q should enter quit confirmation while users are present")
	}

	next, cmd := m.Update(key('n'))
	if cmd != nil {
		t.Fatal("declining should not quit")
	}
	if next.(model).confirmingQuit {
		t.Fatal("declining should leave confirmation mode")
	}
}

// The view reflects whether the owner has joined.
func TestOwnerStatusDisplayed(t *testing.T) {
	reg := registry.New()
	m := newModel(reg, "g", "x")
	if !strings.Contains(m.View(), "awaiting") {
		t.Fatalf("view should show the owner is awaited before joining:\n%s", m.View())
	}

	reg.ClaimOwner()
	m.refresh()
	if !strings.Contains(m.View(), "joined") {
		t.Fatalf("view should show the owner joined:\n%s", m.View())
	}
}

// With no one connected, quitting is immediate (nothing to confirm).
func TestQuitImmediateWhenEmpty(t *testing.T) {
	m := newModel(registry.New(), "g", "x")
	_, cmd := m.Update(key('q'))
	if cmd == nil {
		t.Fatal("q should quit immediately when no one is connected")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q should return tea.Quit when empty")
	}
}
