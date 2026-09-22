package coauthor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/miccah/social-security/internal/registry"
)

// connectedFile is the name, inside the VM's 9p share, of the newline-separated
// list of connected usernames. The in-VM commit helper reads it at commit time.
const connectedFile = "connected"

// sharePathProvider yields the host directory shared into the VM, valid after the
// VM has started. The VM manager satisfies it.
type sharePathProvider interface {
	SharePath() (string, error)
}

// Writer mirrors the registry's active usernames into the VM share so the commit
// helper can credit the currently connected users. It refreshes on every
// registry change.
type Writer struct {
	reg   registry.Registry
	share sharePathProvider

	path string
	stop chan struct{}
	done chan struct{}
}

// NewWriter returns a Writer that publishes reg's active usernames into the share
// exposed by share.
func NewWriter(reg registry.Registry, share sharePathProvider) *Writer {
	return &Writer{reg: reg, share: share}
}

// Name identifies the co-author writer in lifecycle logs.
func (w *Writer) Name() string { return "coauthor" }

// Start resolves the share path (so it must start after the VM), writes the
// initial connected set, and watches the registry for changes on a goroutine.
func (w *Writer) Start(context.Context) error {
	dir, err := w.share.SharePath()
	if err != nil {
		return fmt.Errorf("coauthor: resolve share path: %w", err)
	}
	w.path = filepath.Join(dir, connectedFile)
	if err := w.write(); err != nil {
		return fmt.Errorf("coauthor: write connected set: %w", err)
	}
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	go w.loop()
	return nil
}

// Stop ends the watch goroutine.
func (w *Writer) Stop(context.Context) error {
	if w.stop != nil {
		close(w.stop)
		<-w.done
		w.stop = nil
	}
	return nil
}

// loop rewrites the connected set on each registry event. Events are coalesced,
// so a single wake reflects the latest state via a fresh read.
func (w *Writer) loop() {
	defer close(w.done)
	events := w.reg.Events()
	for {
		select {
		case <-w.stop:
			return
		case <-events:
			if err := w.write(); err != nil {
				slog.Error("coauthor: write connected set", "err", err)
			}
		}
	}
}

// write publishes the current active usernames to the share.
func (w *Writer) write() error {
	active := w.reg.Active()
	names := make([]string, 0, len(active))
	for _, s := range active {
		names = append(names, s.Username)
	}
	return writeList(w.path, names)
}

// writeList writes items one per line, replacing path atomically so the VM never
// reads a half-written file over 9p.
func writeList(path string, items []string) error {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".connected-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
