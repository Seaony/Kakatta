package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/devfixture"
)

const formalFixtureKey = "local-formal-key"

type formalFixtures struct {
	*devfixture.Fixture
	metadata devfixture.Metadata
}

func newFormalFixtures(parent context.Context) *formalFixtures {
	fixture := devfixture.New(parent, "http://127.0.0.1:19090")
	return &formalFixtures{Fixture: fixture, metadata: fixture.Metadata()}
}

func (f *formalFixtures) close()                { f.Close() }
func (f *formalFixtures) state() map[string]any { return f.State() }
func (f *formalFixtures) handler() http.Handler { return f.Handler() }

func TestFormalPathFixtures(t *testing.T) {
	requireFormalPath(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := newFormalFixtures(ctx)
	defer f.close()
	data, err := json.Marshal(f.metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("KAKATTA_FORMAL_DIR"), "fixture.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:19090")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: f.handler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	}
}
