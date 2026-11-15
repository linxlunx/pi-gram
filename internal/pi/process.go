package pi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ManagerOptions configures the Pi process manager.
type ManagerOptions struct {
	// Binary is the path to the pi executable.
	Binary string
	// Args are the command line arguments (see config.PIArgs).
	Args []string
	// Env is the child environment (includes the API key).
	Env []string
	// Dir is the child working directory (empty = inherit).
	Dir string
	// Logger receives process lifecycle and stderr diagnostics.
	Logger *slog.Logger
	// OnStart is invoked after every successful spawn (initial and
	// restarts), e.g. to restore the last session.
	OnStart func(ctx context.Context, client *Client)
	// CmdFactory overrides process creation (used by tests).
	CmdFactory func() (*exec.Cmd, error)

	RestartBaseDelay time.Duration
	RestartMaxDelay  time.Duration
	// StableAfter: a run longer than this resets the restart backoff.
	StableAfter time.Duration
}

// Manager starts and supervises the Pi RPC child process. If Pi exits
// unexpectedly it is restarted with exponential backoff.
type Manager struct {
	opts   ManagerOptions
	logger *slog.Logger

	mu         sync.Mutex
	client     *Client
	cmd        *exec.Cmd
	stderrTail *stderrTail
	startedAt  time.Time
	stopping   bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager creates a manager. Call Start to launch the process.
func NewManager(opts ManagerOptions) *Manager {
	if opts.RestartBaseDelay <= 0 {
		opts.RestartBaseDelay = 1 * time.Second
	}
	if opts.RestartMaxDelay <= 0 {
		opts.RestartMaxDelay = 30 * time.Second
	}
	if opts.StableAfter <= 0 {
		opts.StableAfter = 60 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{opts: opts, logger: opts.Logger}
}

// Start launches the supervision loop. It returns after the first
// successful spawn (or when ctx is cancelled). The loop keeps
// restarting Pi until Stop is called.
func (m *Manager) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runLoop(ctx)
	}()

	// Wait for the first successful spawn.
	deadline := time.Now().Add(30 * time.Second)
	for {
		m.mu.Lock()
		started := m.cmd != nil
		m.mu.Unlock()
		if started {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("pi process did not start within 30s (check PI_BINARY and logs)")
		}
		select {
		case <-parent.Done():
			m.cancel()
			return parent.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *Manager) runLoop(ctx context.Context) {
	backoff := m.opts.RestartBaseDelay
	for {
		if m.isStopping() || ctx.Err() != nil {
			return
		}
		startErr := m.spawn()
		if startErr != nil {
			m.logger.Error("failed to start pi process", "error", startErr)
		} else {
			if m.isStopping() || ctx.Err() != nil {
				// Stop was requested while spawning; terminate the
				// fresh child instead of supervising it.
				m.killCurrent()
				return
			}
			if m.opts.OnStart != nil {
				client := m.Client()
				go func() {
					hctx, hcancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer hcancel()
					m.opts.OnStart(hctx, client)
				}()
			}
			// Wait for the child to exit.
			waitErr := m.cmd.Wait()
			uptime := time.Since(m.startedAt)
			if uptime > m.opts.StableAfter {
				backoff = m.opts.RestartBaseDelay
			}
			m.mu.Lock()
			m.cmd = nil
			m.client = nil
			tail := m.stderrTail
			m.stderrTail = nil
			m.mu.Unlock()

			m.logger.Warn("pi process exited",
				"uptime", uptime.String(),
				"error", waitErrString(waitErr))
			m.logQuickExitStderr(tail, uptime)
			if m.isStopping() || ctx.Err() != nil {
				return
			}
		}
		// Backoff before (re)starting.
		m.logger.Info("restarting pi process", "delay", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if m.isStopping() {
			return
		}
		if backoff *= 2; backoff > m.opts.RestartMaxDelay {
			backoff = m.opts.RestartMaxDelay
		}
	}
}

// spawn creates pipes, starts the child process and hooks up a client.
func (m *Manager) spawn() error {
	var cmd *exec.Cmd
	if m.opts.CmdFactory != nil {
		c, err := m.opts.CmdFactory()
		if err != nil {
			return err
		}
		cmd = c
	} else {
		cmd = exec.Command(m.opts.Binary, m.opts.Args...)
	}
	if cmd.Env == nil {
		cmd.Env = m.opts.Env
	}
	if m.opts.Dir != "" {
		cmd.Dir = m.opts.Dir
	}
	// Own process group so a kill removes the whole tree (pi may spawn
	// child processes while working).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closePipe(stdinR, stdinW)
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closePipe(stdinR, stdinW)
		closePipe(stdoutR, stdoutW)
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		closePipe(stdinR, stdinW)
		closePipe(stdoutR, stdoutW)
		closePipe(stderrR, stderrW)
		return fmt.Errorf("start pi process: %w", err)
	}

	// The child holds its ends; drop ours so EOF propagates on exit.
	if err := stdinR.Close(); err != nil {
		m.logger.Debug("close pi stdin read end", "error", err)
	}
	if err := stdoutW.Close(); err != nil {
		m.logger.Debug("close pi stdout write end", "error", err)
	}
	if err := stderrW.Close(); err != nil {
		m.logger.Debug("close pi stderr write end", "error", err)
	}

	client := NewClient(m.logger, stdinW, stdoutR)
	tail := newStderrTail()
	go client.Run()
	go drainStderr(m.logger, stderrR, tail)

	m.mu.Lock()
	m.cmd = cmd
	m.client = client
	m.stderrTail = tail
	m.startedAt = time.Now()
	m.mu.Unlock()

	m.logger.Info("pi process started", "pid", cmd.Process.Pid)
	return nil
}

// Client returns the current Pi client, or nil while the process is
// down or restarting.
func (m *Manager) Client() *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// Running reports whether the Pi process is alive.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// Busy reports whether the Pi agent is running a task.
func (m *Manager) Busy() bool {
	c := m.Client()
	return c != nil && c.Busy()
}

// Stop shuts the Pi process down gracefully: the supervision loop is
// stopped first so no respawn can race the shutdown, then stdin is
// closed so Pi sees EOF and exits; if it does not exit in time the
// whole process group is killed.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopping = true
	client := m.client
	cmd := m.cmd
	m.mu.Unlock()

	// Halt the supervision loop first; it must not restart Pi while we
	// are shutting down.
	if m.cancel != nil {
		m.cancel()
	}
	if client != nil {
		_ = client.Close()
		// Give Pi a grace period to exit on its own after stdin EOF.
		select {
		case <-client.Done():
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	if cmd != nil && cmd.Process != nil {
		_ = killGroup(cmd)
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// isStopping reports whether Stop has been requested.
func (m *Manager) isStopping() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopping
}

// killCurrent terminates the current child process group, if any.
func (m *Manager) killCurrent() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = killGroup(cmd)
	}
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// Negative pid targets the whole process group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return syscall.Kill(-pid, syscall.SIGKILL)
	}
	time.Sleep(500 * time.Millisecond)
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// quickExitWindow: a Pi process that dies faster than this is treated
// as a startup failure and its stderr tail is logged at ERROR level.
const quickExitWindow = 30 * time.Second

// stderrTailMaxLines is how many trailing stderr lines are retained.
const stderrTailMaxLines = 30

// stderrTail records the last lines the child wrote to stderr so the
// failure reason is available when the process dies quickly. Pi writes
// its diagnostics to stderr; the gateway logs each line at debug level
// and replays the tail at error level on quick exits.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	done  chan struct{}
	once  sync.Once
}

func newStderrTail() *stderrTail {
	return &stderrTail{done: make(chan struct{})}
}

func (t *stderrTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > stderrTailMaxLines {
		t.lines = t.lines[len(t.lines)-stderrTailMaxLines:]
	}
}

func (t *stderrTail) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.lines))
	copy(out, t.lines)
	return out
}

func (t *stderrTail) close() {
	t.once.Do(func() { close(t.done) })
}

// logQuickExitStderr logs the captured stderr tail when a process died
// suspiciously fast, so startup failures are visible at the default
// log level.
func (m *Manager) logQuickExitStderr(tail *stderrTail, uptime time.Duration) {
	if tail == nil || uptime >= quickExitWindow {
		return
	}
	// Give the drain goroutine a moment to capture the final lines.
	select {
	case <-tail.done:
	case <-time.After(250 * time.Millisecond):
	}
	lines := tail.snapshot()
	if len(lines) == 0 {
		return
	}
	m.logger.Error("pi process exited quickly; recent stderr (usually contains the failure reason)",
		"uptime", uptime.String(),
		"stderr", strings.Join(lines, "\n"))
}

func drainStderr(logger *slog.Logger, r *os.File, tail *stderrTail) {
	defer func() {
		_ = r.Close()
		tail.close()
	}()
	sc := newLineScanner(r)
	for sc.Scan() {
		line := sc.Text()
		logger.Debug("pi stderr", "line", line)
		tail.add(line)
	}
}

func closePipe(rs ...*os.File) {
	for _, f := range rs {
		_ = f.Close()
	}
}

// newLineScanner returns a scanner for diagnostics output with a
// generous buffer (only used for stderr; the protocol stream on stdout
// is read with a strict JSON decoder instead).
func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return sc
}

func waitErrString(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "exit status " + strconv.Itoa(exitErr.ExitCode())
	}
	return err.Error()
}
