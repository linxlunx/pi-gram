package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"pigram/internal/auth"
	"pigram/internal/config"
	"pigram/internal/pi"
	"pigram/internal/queue"
	"pigram/internal/session"
)

// Handler routes incoming Telegram updates and drives Pi.
type Handler struct {
	sender   *Sender
	allow    *auth.Allowlist
	manager  *pi.Manager
	sessions *session.Store
	queue    *queue.Queue
	cfg      *config.Config
	logger   *slog.Logger
}

// NewHandler creates the update handler.
func NewHandler(sender *Sender, allow *auth.Allowlist, manager *pi.Manager, sessions *session.Store, q *queue.Queue, cfg *config.Config, logger *slog.Logger) *Handler {
	return &Handler{
		sender:   sender,
		allow:    allow,
		manager:  manager,
		sessions: sessions,
		queue:    q,
		cfg:      cfg,
		logger:   logger,
	}
}

const (
	helpText = "Pi server agent\n\n" +
		"/ask <message> — send an instruction to Pi\n" +
		"/bash <command> — run a shell command directly\n" +
		"/status — show Pi process status\n" +
		"/new — start a new Pi session\n" +
		"/cancel — abort the current task\n" +
		"/help — show this help\n\n" +
		"Any other text message is sent to Pi as a prompt."

	startText = "👋 Hi! I forward your messages to the Pi agent running on this server.\n\n" + helpText
)

// HandleUpdate processes one Telegram update.
func (h *Handler) HandleUpdate(u *models.Update) {
	ctx := context.Background()
	msg := u.Message
	if msg == nil {
		// Only new private messages are supported.
		return
	}
	if msg.Chat.Type != models.ChatTypePrivate {
		h.logger.Debug("ignoring non-private message", "chat_id", msg.Chat.ID)
		return
	}
	user := msg.From
	if user == nil || user.IsBot {
		return
	}
	if !h.allow.Allowed(user.ID) {
		h.logger.Warn("unauthorized access attempt",
			"user_id", user.ID,
			"username", user.Username,
			"chat_id", msg.Chat.ID)
		h.sender.SendPlain(ctx, msg.Chat.ID, "⛔ Access denied.")
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		h.sender.SendPlain(ctx, msg.Chat.ID, "Only text messages are supported for now.")
		return
	}

	h.logger.Info("accepted message",
		"user_id", user.ID,
		"chat_id", msg.Chat.ID,
		"command", commandName(text))

	cmd, args := parseCommand(text)
	switch cmd {
	case "start":
		h.sender.SendPlain(ctx, msg.Chat.ID, startText)
	case "help":
		h.sender.SendPlain(ctx, msg.Chat.ID, helpText)
	case "status":
		h.handleStatus(ctx, msg.Chat.ID)
	case "new":
		h.handleNew(ctx, msg.Chat.ID)
	case "cancel":
		h.handleCancel(ctx, msg.Chat.ID)
	case "ask":
		if args == "" {
			h.sender.SendPlain(ctx, msg.Chat.ID, "Usage: /ask <message>")
			return
		}
		h.submit(ctx, msg.Chat.ID, h.promptTask(msg.Chat.ID, args))
	case "bash":
		if args == "" {
			h.sender.SendPlain(ctx, msg.Chat.ID, "Usage: /bash <command>")
			return
		}
		if reason, bad := CheckBashCommand(args); bad {
			h.sender.SendPlain(ctx, msg.Chat.ID, "🛑 Refused: "+reason)
			return
		}
		h.submit(ctx, msg.Chat.ID, h.bashTask(msg.Chat.ID, args))
	case "":
		// Ordinary text message: treat as a natural-language prompt.
		h.submit(ctx, msg.Chat.ID, h.promptTask(msg.Chat.ID, text))
	default:
		h.sender.SendPlain(ctx, msg.Chat.ID, "Unknown command. "+helpText)
	}
}

// parseCommand splits "/cmd args" (with an optional @botname suffix)
// into the command name and the remaining arguments. Non-commands
// return ("", text).
func parseCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	rest := text[1:]
	cmd := rest
	var args string
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		cmd = rest[:i]
		args = strings.TrimSpace(rest[i+1:])
	}
	// Strip "@botname" from "/cmd@botname".
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd), args
}

func commandName(text string) string {
	cmd, _ := parseCommand(text)
	if cmd == "" {
		return "<prompt>"
	}
	return "/" + cmd
}

// submit enqueues a task and acknowledges acceptance.
func (h *Handler) submit(ctx context.Context, chatID int64, t queue.Task) {
	if err := h.queue.Submit(t); err != nil {
		if errors.Is(err, queue.ErrQueueFull) {
			h.sender.SendPlain(ctx, chatID, "⚠️ Task queue is full, please wait a moment.")
			return
		}
		h.logger.Error("queue submit failed", "error", err)
		return
	}
	pos := h.queue.Len()
	note := "📥 Task accepted."
	if pos > 1 {
		note = fmt.Sprintf("📥 Task accepted (position %d in queue).", pos)
	}
	h.sender.SendPlain(ctx, chatID, note)
}

// handleStatus reports Pi process state.
func (h *Handler) handleStatus(ctx context.Context, chatID int64) {
	client := h.manager.Client()
	if client == nil {
		h.sender.SendPlain(ctx, chatID, "🔴 Pi process: not running (will restart automatically)")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.CommandTimeout)
	defer cancel()
	st, err := client.State(ctx)
	if err != nil {
		h.sender.SendPlain(ctx, chatID, "🔴 Pi process: starting/restarting\n(state query failed: "+err.Error()+")")
		return
	}
	busy := "no"
	if st.IsStreaming || client.Busy() {
		busy = "yes"
	}
	model := st.ModelString()
	if model == "" {
		model = "(unknown)"
	}
	sess := st.SessionName
	if sess == "" {
		sess = st.SessionFile
	}
	if sess == "" {
		sess = st.SessionID
	}
	h.sender.SendPlain(ctx, chatID, fmt.Sprintf(
		"🟢 Pi status\nBusy: %s\nModel: %s\nSession: %s\nMessages: %d (pending: %d)\nQueue: %d task(s)",
		busy, model, sess, st.MessageCount, st.PendingMessageCount, h.queue.Len()))
}

// handleNew starts a fresh Pi session for the chat.
func (h *Handler) handleNew(ctx context.Context, chatID int64) {
	client := h.manager.Client()
	if client == nil {
		h.sender.SendPlain(ctx, chatID, "Pi process is not running yet, try again shortly.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.CommandTimeout)
	defer cancel()
	st, err := client.State(ctx)
	if err != nil {
		h.sender.SendPlain(ctx, chatID, "❌ Could not query Pi state: "+err.Error())
		return
	}
	if st.IsStreaming || client.Busy() {
		h.sender.SendPlain(ctx, chatID, "Pi is busy. Use /cancel first if you want to stop the current task.")
		return
	}
	cancelled, err := client.NewSession(ctx)
	if err != nil {
		h.sender.SendPlain(ctx, chatID, "❌ Failed to start new session: "+err.Error())
		return
	}
	if cancelled {
		h.sender.SendPlain(ctx, chatID, "⚠️ New session was cancelled by an extension.")
		return
	}
	h.recordSession(ctx, chatID, client)
	h.sender.SendPlain(ctx, chatID, "🆕 New session started.")
}

// handleCancel aborts the current Pi task.
func (h *Handler) handleCancel(ctx context.Context, chatID int64) {
	client := h.manager.Client()
	if client == nil {
		h.sender.SendPlain(ctx, chatID, "Pi process is not running.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.CommandTimeout)
	defer cancel()
	if err := client.Abort(ctx); err != nil {
		h.sender.SendPlain(ctx, chatID, "❌ Abort failed: "+err.Error())
		return
	}
	h.sender.SendPlain(ctx, chatID, "🛑 Abort requested.")
}

// ensureSession points the Pi process at the chat's session. If the
// chat has no stored session yet, the current one is adopted.
func (h *Handler) ensureSession(ctx context.Context, chatID int64, client *pi.Client) error {
	rec := h.sessions.Get(chatID)
	st, err := client.State(ctx)
	if err != nil {
		return fmt.Errorf("query pi state: %w", err)
	}
	if rec.PiSessionFile == "" {
		return h.recordSession(ctx, chatID, client)
	}
	if st.SessionFile == rec.PiSessionFile {
		return nil
	}
	if err := client.SwitchSession(ctx, rec.PiSessionFile); err != nil {
		return fmt.Errorf("switch to session %s: %w", rec.PiSessionFile, err)
	}
	return nil
}

// recordSession persists the Pi session currently active for a chat.
func (h *Handler) recordSession(ctx context.Context, chatID int64, client *pi.Client) error {
	st, err := client.State(ctx)
	if err != nil {
		return err
	}
	rec := session.ChatSession{
		PiSessionFile: st.SessionFile,
		PiSessionID:   st.SessionID,
		LastActivity:  time.Now(),
	}
	if err := h.sessions.Update(chatID, rec); err != nil {
		return err
	}
	if st.SessionFile != "" {
		return h.sessions.SetCurrent(st.SessionFile)
	}
	return nil
}

// promptTask returns a queue task that runs a prompt through Pi and
// delivers the final assistant response to the chat.
func (h *Handler) promptTask(chatID int64, message string) queue.Task {
	return func(ctx context.Context) {
		client := h.manager.Client()
		if client == nil {
			h.sender.SendPlain(ctx, chatID, "❌ Pi process is not running. It restarts automatically; please retry shortly.")
			return
		}
		if err := h.ensureSession(ctx, chatID, client); err != nil {
			h.sender.SendPlain(ctx, chatID, "❌ Session error: "+err.Error())
			return
		}

		runCtx, cancel := context.WithTimeout(ctx, h.cfg.PromptTimeout)
		defer cancel()

		tools := 0
		res, err := client.RunPrompt(runCtx, message, func(ev pi.Event) {
			// Lightweight progress notes for the first few tool runs.
			if ev.Type == pi.EventToolStart && ev.ToolName != "" && tools < 3 {
				tools++
				h.sender.SendPlain(ctx, chatID, "🔧 running: "+ev.ToolName)
			}
		})
		if err != nil {
			h.reportPromptError(ctx, chatID, err, res.Text)
			return
		}

		text := res.Text
		if text == "" {
			text = client.LastAssistantText(ctx)
		}
		if text == "" {
			if res.Disposition == "handled" {
				text = "(prompt was handled internally by an extension)"
			} else {
				text = "(Pi finished without a text response)"
			}
		}
		if res.Aborted {
			text = "🛑 Task aborted.\n\n" + text
		}
		h.sender.Send(ctx, chatID, text)
		h.logger.Info("prompt completed", "chat_id", chatID, "disposition", res.Disposition, "aborted", res.Aborted)
	}
}

// reportPromptError notifies the user about a failed prompt run.
func (h *Handler) reportPromptError(ctx context.Context, chatID int64, err error, partial string) {
	h.logger.Error("prompt failed", "chat_id", chatID, "error", err)
	var note string
	switch {
	case errors.Is(err, pi.ErrClosed):
		note = "❌ The Pi process terminated during the task. It will restart automatically; try again shortly."
	case errors.Is(err, context.DeadlineExceeded):
		note = "⏱️ The task timed out. Partial response (if any) below.\n\n"
		if partial != "" {
			h.sender.Send(ctx, chatID, note+partial)
			return
		}
		h.sender.SendPlain(ctx, chatID, note)
		return
	default:
		note = "❌ Task failed: " + err.Error()
	}
	if partial != "" {
		h.sender.Send(ctx, chatID, note+"\n\nLast partial response:\n\n"+partial)
		return
	}
	h.sender.SendPlain(ctx, chatID, note)
}

// bashTask returns a queue task that runs a direct shell command
// through the Pi RPC bash command.
func (h *Handler) bashTask(chatID int64, command string) queue.Task {
	return func(ctx context.Context) {
		client := h.manager.Client()
		if client == nil {
			h.sender.SendPlain(ctx, chatID, "❌ Pi process is not running. It restarts automatically; please retry shortly.")
			return
		}
		// Defense in depth: re-check at execution time.
		if reason, bad := CheckBashCommand(command); bad {
			h.sender.SendPlain(ctx, chatID, "🛑 Refused: "+reason)
			return
		}

		bashCtx, cancel := context.WithTimeout(ctx, h.cfg.BashTimeout)
		defer cancel()
		res, err := client.Bash(bashCtx, command)
		if err != nil {
			h.logger.Error("bash command failed", "chat_id", chatID, "error", err)
			switch {
			case errors.Is(err, pi.ErrClosed):
				h.sender.SendPlain(ctx, chatID, "❌ The Pi process terminated during the command. It will restart automatically.")
			case errors.Is(err, context.DeadlineExceeded):
				h.sender.SendPlain(ctx, chatID, "⏱️ The command timed out.")
			default:
				h.sender.SendPlain(ctx, chatID, "❌ Command failed: "+err.Error())
			}
			return
		}

		out := res.Output
		note := ""
		if res.Truncated {
			note = "\n\n(output truncated; full output: " + res.FullOutputPath + ")"
		}
		if res.Cancelled {
			note += "\n(command was cancelled)"
		}
		msg := fmt.Sprintf("$ %s\n\n%s", command, out)
		if out == "" {
			msg = fmt.Sprintf("$ %s\n\n(no output)", command)
		}
		if res.ExitCode != 0 {
			msg += fmt.Sprintf("\n\nexit code: %d", res.ExitCode)
		}
		msg += note
		h.sender.SendPlain(ctx, chatID, msg)
	}
}
