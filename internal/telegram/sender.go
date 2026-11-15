package telegram

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Sender delivers messages to Telegram chats, handling the message
// length limit and MarkdownV2 formatting.
type Sender struct {
	bot    *bot.Bot
	logger *slog.Logger
}

// NewSender creates a sender around a bot API client.
func NewSender(b *bot.Bot, logger *slog.Logger) *Sender {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sender{bot: b, logger: logger}
}

// chunkLimit leaves headroom below Telegram's 4096 limit for escaping.
const chunkLimit = 3800

// Send delivers text to a chat using MarkdownV2. If Telegram rejects
// the formatted message, it is retried as plain text.
func (s *Sender) Send(ctx context.Context, chatID int64, text string) {
	if text == "" {
		return
	}
	for _, chunk := range SplitAndEscapeMarkdownV2(text, chunkLimit) {
		if err := s.sendWithRetry(ctx, chatID, chunk, models.ParseModeMarkdown); err != nil {
			if err2 := s.sendWithRetry(ctx, chatID, chunk, ""); err2 != nil {
				s.logger.Error("failed to deliver telegram message", "chat_id", chatID, "error", err2)
			}
		}
	}
}

// SendPlain delivers text to a chat without any parse mode.
func (s *Sender) SendPlain(ctx context.Context, chatID int64, text string) {
	if text == "" {
		return
	}
	for _, chunk := range splitPlainText(text, chunkLimit) {
		if err := s.sendWithRetry(ctx, chatID, chunk, ""); err != nil {
			s.logger.Error("failed to deliver telegram message", "chat_id", chatID, "error", err)
		}
	}
}

func (s *Sender) send(ctx context.Context, chatID int64, text string, parseMode models.ParseMode) error {
	params := &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}
	if parseMode != "" {
		params.ParseMode = parseMode
	}
	_, err := s.bot.SendMessage(ctx, params)
	return err
}

// sendWithRetry retries once on transient failures.
func (s *Sender) sendWithRetry(ctx context.Context, chatID int64, text string, parseMode models.ParseMode) error {
	err := s.send(ctx, chatID, text, parseMode)
	if err == nil {
		return nil
	}
	s.logger.Warn("telegram send failed, retrying", "chat_id", chatID, "error", err)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(750 * time.Millisecond):
	}
	return s.send(ctx, chatID, text, parseMode)
}
