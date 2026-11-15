// Package pi manages the Pi coding agent subprocess running in RPC
// mode and implements the JSONL command/response/event protocol over
// the child's stdin and stdout.
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned when the Pi process closed its stdout (it
// exited or the connection was shut down) while a command was pending.
var ErrClosed = errors.New("pi rpc connection closed")

// RunResult is the outcome of a completed prompt run.
type RunResult struct {
	// Disposition is the prompt response disposition ("started",
	// "queued" or "handled").
	Disposition string
	// Aborted reports whether the run ended because it was aborted.
	Aborted bool
	// Text is the text of the last assistant message observed.
	Text string
}

// subscription receives the event stream of the Pi session.
type subscription struct {
	key int64
	ch  chan Event
}

// Client speaks the Pi RPC protocol over a child process's
// stdin/stdout pipes.
type Client struct {
	logger *slog.Logger

	wmu sync.Mutex
	w   *bufio.Writer

	stdinCloser io.Closer
	stdout      io.Reader

	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	pending map[string]chan *Response
	subs    map[int64]*subscription

	busy       atomic.Bool
	sessionF   atomic.Value // string
	reqCounter atomic.Int64
}

// NewClient creates a client that writes commands to stdin and reads
// protocol records from stdout. Call Run to start the read loop.
func NewClient(logger *slog.Logger, stdin io.WriteCloser, stdout io.Reader) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		logger:      logger,
		w:           bufio.NewWriter(stdin),
		stdinCloser: stdin,
		stdout:      stdout,
		done:        make(chan struct{}),
		pending:     make(map[string]chan *Response),
		subs:        make(map[int64]*subscription),
	}
	c.sessionF.Store("")
	return c
}

// Run consumes Pi stdout until EOF or a read error. It must be started
// exactly once, in its own goroutine.
func (c *Client) Run() {
	dec := json.NewDecoder(c.stdout)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err != io.EOF && c.ctxActive() {
				c.logger.Error("pi stdout read error", "error", err)
			}
			break
		}
		c.handleRecord(raw)
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		pending := c.pending
		subs := c.subs
		c.pending = make(map[string]chan *Response)
		c.subs = make(map[int64]*subscription)
		c.mu.Unlock()
		for id, ch := range pending {
			ch <- &Response{ID: id, Type: "response", Success: false, Error: ErrClosed.Error()}
			close(ch)
		}
		for _, s := range subs {
			close(s.ch)
		}
		close(c.done)
	})
}

// ctxActive reports whether the client was not closed via Close().
func (c *Client) ctxActive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *Client) handleRecord(raw json.RawMessage) {
	var env struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		c.logger.Warn("pi record parse error", "error", err)
		return
	}
	switch env.Type {
	case "response":
		var resp Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			c.logger.Warn("pi response parse error", "error", err)
			return
		}
		if resp.Command == CmdGetState && resp.Success {
			var st SessionState
			if err := json.Unmarshal(resp.Data, &st); err == nil && st.SessionFile != "" {
				c.sessionF.Store(st.SessionFile)
			}
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if !ok {
			c.logger.Warn("pi response without pending request", "command", resp.Command)
			return
		}
		ch <- &resp
		close(ch)
	default:
		c.handleEvent(raw, env.Type)
	}
}

func (c *Client) handleEvent(raw json.RawMessage, typ string) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.logger.Warn("pi event parse error", "error", err)
		return
	}
	switch typ {
	case EventAgentStart:
		c.busy.Store(true)
	case EventAgentSettled:
		c.busy.Store(false)
	}
	c.mu.Lock()
	subs := make([]*subscription, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	c.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			c.logger.Warn("event dropped, subscriber too slow", "type", typ)
		}
	}
}

// Subscribe registers for the event stream. The returned channel is
// closed when the connection ends; call Unsubscribe to detach early.
func (c *Client) Subscribe() *subscription {
	ch := make(chan Event, 128)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Reuse monotonic sub keys independent of request IDs.
	id := c.reqCounter.Add(1) * -1 // negative to avoid clash space
	s := &subscription{ch: ch}
	c.subs[id] = s
	s.key = id
	return s
}

func (c *Client) Unsubscribe(s *subscription) {
	c.mu.Lock()
	delete(c.subs, s.key)
	c.mu.Unlock()
}

// Done is closed when the connection ends (process exited).
func (c *Client) Done() <-chan struct{} { return c.done }

// Busy reports whether an agent run is active.
func (c *Client) Busy() bool { return c.busy.Load() }

// SessionFile returns the last known active Pi session file.
func (c *Client) SessionFile() string {
	v, _ := c.sessionF.Load().(string)
	return v
}

// Close closes the child's stdin, signalling EOF so Pi exits cleanly.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdinCloser == nil {
		return nil
	}
	err := c.stdinCloser.Close()
	c.stdinCloser = nil
	return err
}

func (c *Client) send(ctx context.Context, cmd Command) (*Response, error) {
	if cmd.ID == "" {
		cmd.ID = fmt.Sprintf("req-%d", c.reqCounter.Add(1))
	}
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[cmd.ID] = ch
	c.mu.Unlock()

	data, err := json.Marshal(cmd)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, cmd.ID)
		c.mu.Unlock()
		return nil, fmt.Errorf("encode command: %w", err)
	}

	c.wmu.Lock()
	if _, err := c.w.Write(data); err == nil {
		err = c.w.WriteByte('\n')
	}
	if err == nil {
		err = c.w.Flush()
	}
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, cmd.ID)
		c.mu.Unlock()
		return nil, fmt.Errorf("write command to pi stdin: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, cmd.ID)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

// request sends a command and converts a failed response into an error.
func (c *Client) request(ctx context.Context, cmd Command) (*Response, error) {
	resp, err := c.send(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		msg := "command failed"
		if resp.Error != "" {
			msg = resp.Error
		}
		return resp, fmt.Errorf("pi %s: %s", cmd.Type, msg)
	}
	return resp, nil
}

// State queries the current Pi session state.
func (c *Client) State(ctx context.Context) (*SessionState, error) {
	resp, err := c.request(ctx, Command{Type: CmdGetState})
	if err != nil {
		return nil, err
	}
	var st SessionState
	if err := json.Unmarshal(resp.Data, &st); err != nil {
		return nil, fmt.Errorf("decode get_state data: %w", err)
	}
	if st.SessionFile != "" {
		c.sessionF.Store(st.SessionFile)
	}
	return &st, nil
}

// RunPrompt sends a prompt and waits for the agent to settle. The
// subscription is installed before the prompt is sent so that fast
// completions are not missed.
func (c *Client) RunPrompt(ctx context.Context, message string, onEvent func(Event)) (RunResult, error) {
	sub := c.Subscribe()
	defer c.Unsubscribe(sub)

	resp, err := c.request(ctx, Command{Type: CmdPrompt, Message: message})
	if err != nil {
		return RunResult{}, err
	}
	var d PromptDispositionData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return RunResult{Disposition: "started"}, nil
	}
	result := RunResult{Disposition: d.Disposition}
	if d.Disposition == "handled" {
		// An extension consumed the prompt; no run will start and no
		// agent_settled will arrive for it.
		return result, nil
	}

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				return result, ErrClosed
			}
			switch ev.Type {
			case EventMessageEnd:
				if text := messageText(ev.Message); text != "" {
					result.Text = text
				}
			case EventAgentSettled:
				result.Aborted = ev.Aborted
				return result, nil
			}
			if onEvent != nil {
				onEvent(ev)
			}
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// Bash executes a shell command directly through the Pi RPC bash
// command and returns the final result. The response of a bash command
// is emitted only after the command finished.
func (c *Client) Bash(ctx context.Context, command string) (*BashResultData, error) {
	resp, err := c.request(ctx, Command{Type: CmdBash, Command: command})
	if err != nil {
		return nil, err
	}
	var d BashResultData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, fmt.Errorf("decode bash data: %w", err)
	}
	return &d, nil
}

// Abort cancels the current agent operation. Pi waits for the session
// to become idle before responding.
func (c *Client) Abort(ctx context.Context) error {
	_, err := c.request(ctx, Command{Type: CmdAbort})
	return err
}

// NewSession starts a fresh Pi session.
func (c *Client) NewSession(ctx context.Context) (bool, error) {
	resp, err := c.request(ctx, Command{Type: CmdNewSession})
	if err != nil {
		return false, err
	}
	var d NewSessionData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return false, nil
	}
	if d.Cancelled {
		return true, nil
	}
	// Refresh the tracked session file.
	if _, err := c.State(ctx); err != nil {
		return false, nil
	}
	return false, nil
}

// SwitchSession makes an existing Pi session file active.
func (c *Client) SwitchSession(ctx context.Context, path string) error {
	if _, err := c.request(ctx, Command{Type: CmdSwitchSession, SessionPath: path}); err != nil {
		return err
	}
	c.sessionF.Store(path)
	// Verify and refresh state (cheap, and keeps tracked state honest).
	if _, err := c.State(ctx); err != nil {
		return nil
	}
	return nil
}

// LastAssistantText asks Pi for the last assistant text, used as a
// fallback when no message_end carried text during a run.
func (c *Client) LastAssistantText(ctx context.Context) string {
	resp, err := c.request(ctx, Command{Type: CmdLastAssistant})
	if err != nil {
		return ""
	}
	var d LastAssistantData
	if err := json.Unmarshal(resp.Data, &d); err == nil && d.Text != "" {
		return d.Text
	}
	// Some versions may return a bare string.
	var s string
	if err := json.Unmarshal(resp.Data, &s); err == nil {
		return s
	}
	return ""
}

// messageText decodes a message payload and returns its text content
// for assistant messages.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg AgentMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	if msg.Role != "assistant" {
		return ""
	}
	return strings.TrimSpace(msg.Text())
}
