package pi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakePi simulates the Pi RPC side over in-memory pipes. The handler
// is invoked for every command line and returns the records to write
// back to stdout.
type fakePi struct {
	stdin   io.ReadCloser // commands written by the client
	stdout  io.WriteCloser
	handler func(line string) []string
}

func newFakePi(handler func(line string) []string) (*fakePi, *Client) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	f := &fakePi{stdin: stdinReader, stdout: stdoutWriter, handler: handler}
	go f.serve()
	client := NewClient(discardLogger(), stdinWriter, stdoutReader)
	go client.Run()
	return f, client
}

func (f *fakePi) serve() {
	sc := bufio.NewScanner(f.stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		for _, rec := range f.handler(sc.Text()) {
			if _, err := f.stdout.Write([]byte(rec + "\n")); err != nil {
				return
			}
		}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func context2s() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func TestClientPromptRun(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`,
			`{"type":"agent_start"}`,
			`{"type":"message_end","message":{"role":"user","content":"hi"}}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Nginx is running."}]}}`,
			`{"type":"agent_settled","aborted":false}`,
		}
	})

	ctx, cancel := context2s()
	defer cancel()
	res, err := client.RunPrompt(ctx, "Check whether Nginx is running.", nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if res.Disposition != "started" {
		t.Fatalf("disposition = %q, want started", res.Disposition)
	}
	if res.Text != "Nginx is running." {
		t.Fatalf("text = %q, want the assistant response", res.Text)
	}
}

func TestClientPromptRunCorrelatesIDs(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		if id == "" {
			t.Errorf("command without id: %s", line)
		}
		return []string{
			`{"id":"` + id + `","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
			`{"type":"agent_settled","aborted":false}`,
		}
	})
	ctx, cancel := context2s()
	defer cancel()
	res, err := client.RunPrompt(ctx, "do it", nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestClientHandledDispositionSkipsSettled(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`,
		}
	})
	ctx, cancel := context2s()
	defer cancel()
	res, err := client.RunPrompt(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if res.Disposition != "handled" {
		t.Fatalf("disposition = %q, want handled", res.Disposition)
	}
}

func TestClientBash(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"bash","success":true,"data":{"output":"Filesystem  Size","exitCode":0,"cancelled":false,"truncated":false}}`,
		}
	})
	ctx, cancel := context2s()
	defer cancel()
	res, err := client.Bash(ctx, "df -h")
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if res.Output != "Filesystem  Size" || res.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestClientCommandFailure(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"bash","success":false,"error":"boom"}`,
		}
	})
	ctx, cancel := context2s()
	defer cancel()
	if _, err := client.Bash(ctx, "false"); err == nil {
		t.Fatal("expected error for failed response")
	}
}

func TestClientStateUpdatesSessionFile(t *testing.T) {
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"abc","sessionFile":"/tmp/abc.jsonl","isStreaming":false,"messageCount":2,"pendingMessageCount":0}}`,
		}
	})
	ctx, cancel := context2s()
	defer cancel()
	st, err := client.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.SessionFile != "/tmp/abc.jsonl" {
		t.Fatalf("session file = %q", st.SessionFile)
	}
	if got := client.SessionFile(); got != "/tmp/abc.jsonl" {
		t.Fatalf("tracked session file = %q", got)
	}
}

func TestClientProcessExitFailsPending(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(discardLogger(), stdinWriter, stdoutReader)
	go client.Run()

	// Respond to the command, then close stdout to simulate a crash.
	errCh := make(chan error, 1)
	go func() {
		buf := bufio.NewReader(stdinReader)
		if _, err := buf.ReadString('\n'); err != nil {
			errCh <- err
			return
		}
		stdoutWriter.Write([]byte(`{"id":"x","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}` + "\n"))
		stdoutWriter.Close() // process died
	}()

	ctx, cancel := context2s()
	defer cancel()
	_, err := client.RunPrompt(ctx, "hi", nil)
	if err == nil {
		t.Fatal("expected error when the pi process exits mid-run")
	}
}

func TestClientEvents(t *testing.T) {
	var events []string
	_, client := newFakePi(func(line string) []string {
		id := extractJSONString(line, "id")
		return []string{
			`{"id":"` + id + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"s"}}`,
			`{"type":"agent_start"}`,
			`{"type":"agent_settled","aborted":false}`,
		}
	})
	sub := client.Subscribe()
	defer client.Unsubscribe(sub)

	ctx, cancel := context2s()
	defer cancel()
	if _, err := client.State(ctx); err != nil {
		t.Fatalf("State: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for len(events) < 2 {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				t.Fatal("subscription closed unexpectedly")
			}
			events = append(events, ev.Type)
		case <-deadline:
			t.Fatalf("timed out waiting for events, got %v", events)
		}
	}
	if events[0] != "agent_start" || events[1] != "agent_settled" {
		t.Fatalf("events = %v", events)
	}
}

// extractJSONString pulls a top-level string field from a JSON line.
func extractJSONString(line, field string) string {
	needle := `"` + field + `":"`
	i := indexOf(line, needle)
	if i < 0 {
		return ""
	}
	rest := line[i+len(needle):]
	for j := 0; j < len(rest); j++ {
		if rest[j] == '"' {
			return rest[:j]
		}
	}
	return ""
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
