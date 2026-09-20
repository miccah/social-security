// Package tunnel manages the public ingress: an ngrok TCP endpoint exposed as a
// net.Listener on the host. It surfaces the public address to the front door and
// control plane and closes on teardown. ngrok is the only public path to the
// front door.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"

	"golang.ngrok.com/ngrok/v2"

	"github.com/miccah/social-security/internal/lifecycle"
)

// authtokenEnv holds the ngrok authtoken the agent authenticates with.
const authtokenEnv = "NGROK_AUTHTOKEN"

// Manager owns the ngrok tunnel lifecycle.
type Manager interface {
	lifecycle.Manager

	// Listener returns the public ngrok listener the front door serves guests
	// on. It is valid after Start.
	Listener() net.Listener

	// URL returns the public endpoint URL guests connect to. It is valid after
	// Start.
	URL() *url.URL
}

type manager struct {
	agent ngrok.Agent
	ln    ngrok.EndpointListener
}

// New returns a tunnel manager. The ngrok endpoint is opened on Start.
func New() Manager { return &manager{} }

func (m *manager) Name() string { return "tunnel" }

// Start authenticates an ngrok agent and opens a public TCP endpoint, keeping
// its listener for the front door to serve. It connects eagerly so a missing
// authtoken, a bad token, or no network fails startup here with a clear error
// rather than lazily on the first guest connection, leaving nothing exposed.
func (m *manager) Start(ctx context.Context) error {
	token := os.Getenv(authtokenEnv)
	if token == "" {
		return fmt.Errorf("%s not set: an ngrok authtoken is required for public ingress", authtokenEnv)
	}

	agent, err := ngrok.NewAgent(ngrok.WithAuthtoken(token))
	if err != nil {
		return fmt.Errorf("create ngrok agent: %w", err)
	}
	if err := agent.Connect(ctx); err != nil {
		return fmt.Errorf("connect to ngrok: %w", err)
	}

	// An empty tcp:// URL requests an ephemeral public TCP address, which stock
	// ssh clients can reach.
	ln, err := agent.Listen(ctx, ngrok.WithURL("tcp://"))
	if err != nil {
		agent.Disconnect()
		return fmt.Errorf("open ngrok tcp endpoint: %w", err)
	}

	m.agent = agent
	m.ln = ln
	slog.Info("tunnel: public endpoint open", "url", ln.URL().String())
	return nil
}

// Stop closes the public endpoint and disconnects the agent, so the public
// address stops accepting connections on teardown.
func (m *manager) Stop(context.Context) error {
	var errs []error
	if m.ln != nil {
		if err := m.ln.Close(); err != nil {
			errs = append(errs, err)
		}
		m.ln = nil
	}
	if m.agent != nil {
		if err := m.agent.Disconnect(); err != nil {
			errs = append(errs, err)
		}
		m.agent = nil
	}
	return errors.Join(errs...)
}

// Listener returns the public ngrok listener, or nil before Start.
func (m *manager) Listener() net.Listener {
	if m.ln == nil {
		return nil
	}
	return m.ln
}

// URL returns the public endpoint URL, or nil before Start.
func (m *manager) URL() *url.URL {
	if m.ln == nil {
		return nil
	}
	return m.ln.URL()
}
