package control

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/miccah/social-security/internal/registry"
)

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// Pressing 'a' accepts the selected pending request.
func TestAcceptResolvesSelectedPending(t *testing.T) {
	reg := registry.New()
	req, err := reg.AddPending("alice", "111", "10.0.0.1:2200")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "ssh -p 1337 host")
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

// Pressing 'd' declines the selected pending request and frees the username.
func TestDeclineResolvesSelectedPending(t *testing.T) {
	reg := registry.New()
	req, err := reg.AddPending("bob", "1", "a")
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(reg, "x")
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
	req, _ := reg.AddPending("carol", "1", "a")
	reg.Resolve(req.ID, registry.Accept)
	<-req.Decision()
	kicked := make(chan struct{}, 1)
	reg.Activate(req, func() { kicked <- struct{}{} })

	m := newModel(reg, "x")
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
	first, _ := reg.AddPending("a", "1", "x")
	time.Sleep(time.Millisecond)
	second, _ := reg.AddPending("b", "2", "y")

	m := newModel(reg, "x")
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
	m := newModel(reg, "x")

	reg.AddPending("dave", "1", "a")
	if !m.refresh() {
		t.Fatal("expected the bell when a request arrives")
	}
	if m.refresh() {
		t.Fatal("no bell when nothing new arrived")
	}
}

// Quitting asks for confirmation first, then y ends the session.
func TestQuitRequiresConfirmation(t *testing.T) {
	m := newModel(registry.New(), "x")

	next, cmd := m.Update(key('q'))
	if cmd != nil {
		t.Fatal("q alone should prompt, not quit")
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
	m := newModel(registry.New(), "x")

	next, _ := m.Update(key('q'))
	m = next.(model)

	next, cmd := m.Update(key('n'))
	if cmd != nil {
		t.Fatal("declining should not quit")
	}
	if next.(model).confirmingQuit {
		t.Fatal("declining should leave confirmation mode")
	}
}
