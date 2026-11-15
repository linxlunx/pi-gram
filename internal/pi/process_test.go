package pi

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePiScript responds to every command with a canned get_state
// success response, then exits to simulate a crash.
const fakePiScript = `while IFS= read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionId":"fake","sessionFile":"/tmp/fake-session.jsonl"}}\n' "$id"
  exit 0
done`

func TestManagerRestart(t *testing.T) {
	manager := NewManager(ManagerOptions{
		Logger: discardLogger(),
		CmdFactory: func() (*exec.Cmd, error) {
			return exec.Command("sh", "-c", fakePiScript), nil
		},
		RestartBaseDelay: 100 * time.Millisecond,
		RestartMaxDelay:  500 * time.Millisecond,
		StableAfter:      1 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx)
	}()

	// First request works, then the fake process exits.
	c1 := manager.Client()
	if c1 == nil {
		t.Fatal("no client after start")
	}
	stateCtx, stateCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stateCancel()
	if _, err := c1.State(stateCtx); err != nil {
		t.Fatalf("first state: %v", err)
	}

	// Wait for the manager to restart the process.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c2 := manager.Client()
		if c2 != nil && c2 != c1 {
			stateCtx2, stateCancel2 := context.WithTimeout(context.Background(), 2*time.Second)
			defer stateCancel2()
			if _, err := c2.State(stateCtx2); err != nil {
				t.Fatalf("state after restart: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not restart the pi process in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestManagerStop(t *testing.T) {
	manager := NewManager(ManagerOptions{
		Logger: discardLogger(),
		CmdFactory: func() (*exec.Cmd, error) {
			// A process that stays alive until stdin closes.
			return exec.Command("sh", "-c", "sleep 30"), nil
		},
		RestartBaseDelay: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !manager.Running() {
		t.Fatal("manager should report running")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Running() {
		if time.Now().After(deadline) {
			t.Fatal("manager still reports running after stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if manager.Client() != nil {
		t.Fatal("client should be nil after stop")
	}
}

func TestManagerOnStartHook(t *testing.T) {
	hook := make(chan *Client, 4)
	manager := NewManager(ManagerOptions{
		Logger: discardLogger(),
		CmdFactory: func() (*exec.Cmd, error) {
			return exec.Command("sh", "-c", fakePiScript), nil
		},
		RestartBaseDelay: 100 * time.Millisecond,
		StableAfter:      1 * time.Millisecond,
		OnStart: func(ctx context.Context, client *Client) {
			hook <- client
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx)
	}()
	select {
	case <-hook:
	case <-time.After(5 * time.Second):
		t.Fatal("OnStart hook was not invoked")
	}
}

// syncBuffer is a mutex-protected buffer for log output written from
// multiple goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestManagerLogsStderrOnQuickExit(t *testing.T) {
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	manager := NewManager(ManagerOptions{
		Logger: logger,
		CmdFactory: func() (*exec.Cmd, error) {
			// Fake a Pi that prints its failure to stderr and dies.
			return exec.Command("sh", "-c", `echo "fatal: model not found" >&2; exit 7`), nil
		},
		RestartBaseDelay: 50 * time.Millisecond,
		RestartMaxDelay:  100 * time.Millisecond,
		StableAfter:      1 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		s := buf.String()
		if strings.Contains(s, "exited quickly") && strings.Contains(s, "fatal: model not found") {
			return // success
		}
		if time.Now().After(deadline) {
			t.Fatalf("quick-exit stderr was not logged; log so far:\n%s", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
