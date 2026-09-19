package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/miccah/social-security/internal/lifecycle"
)

// fakeManager records its start/stop calls into a shared log and can be told to
// fail either call.
type fakeManager struct {
	name      string
	failStart bool
	failStop  bool
	events    *[]string
}

func (f *fakeManager) Name() string { return f.name }

func (f *fakeManager) Start(context.Context) error {
	*f.events = append(*f.events, "start:"+f.name)
	if f.failStart {
		return errors.New("start failed")
	}
	return nil
}

func (f *fakeManager) Stop(context.Context) error {
	*f.events = append(*f.events, "stop:"+f.name)
	if f.failStop {
		return errors.New("stop failed")
	}
	return nil
}

// Teardown stops every manager in reverse order and does not abort when a Stop
// fails: every subsystem must get a chance to release its resources.
func TestTeardownReverseOrderContinuesOnError(t *testing.T) {
	var events []string
	ms := []lifecycle.Manager{
		&fakeManager{name: "a", events: &events},
		&fakeManager{name: "b", failStop: true, events: &events},
		&fakeManager{name: "c", events: &events},
	}
	teardown(ms)

	want := []string{"stop:c", "stop:b", "stop:a"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("teardown order = %v, want %v", events, want)
	}
}

// A failed Start stops the sequence and returns only the managers that came up,
// so the caller tears down exactly those.
func TestStartManagersStopsAtFirstFailure(t *testing.T) {
	var events []string
	a := &fakeManager{name: "a", events: &events}
	b := &fakeManager{name: "b", failStart: true, events: &events}
	c := &fakeManager{name: "c", events: &events}

	started, err := startManagers(context.Background(), []lifecycle.Manager{a, b, c})
	if err == nil {
		t.Fatal("expected an error when a manager fails to start")
	}
	if len(started) != 1 || started[0] != a {
		t.Fatalf("started = %v, want [a]", started)
	}
	want := []string{"start:a", "start:b"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("start order = %v, want %v (c must not start)", events, want)
	}
}
