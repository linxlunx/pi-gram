// Command server runs the Telegram webhook gateway for the Pi coding
// agent. It serves Telegram's webhook over HTTPS (behind Nginx),
// supervises a persistent Pi RPC child process, and relays messages
// between Telegram and Pi.
//
// Flags:
//
//	-register-webhook  register the configured webhook with Telegram and exit
//	-delete-webhook    remove the webhook registration and exit
//	-env-file PATH     load KEY=VALUE pairs from PATH before reading the
//	                   environment (useful for one-off commands such as
//	                   -register-webhook outside systemd)
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"

	"pigram/internal/auth"
	"pigram/internal/config"
	"pigram/internal/pi"
	"pigram/internal/queue"
	"pigram/internal/session"
	"pigram/internal/telegram"
)

func main() {
	registerWebhook := flag.Bool("register-webhook", false, "register the webhook with Telegram and exit")
	deleteWebhook := flag.Bool("delete-webhook", false, "delete the webhook registration and exit")
	envFile := flag.String("env-file", "", "load KEY=VALUE pairs from this file before reading the environment")
	flag.Parse()

	logger := newLogger("info")

	if *envFile != "" {
		if err := config.LoadEnvFile(*envFile); err != nil {
			logger.Error("env file error", "path", *envFile, "error", err)
			os.Exit(1)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	logger = newLogger(cfg.LogLevel)

	b, err := bot.New(cfg.TelegramBotToken)
	if err != nil {
		logger.Error("telegram bot client error", "error", err)
		os.Exit(1)
	}

	switch {
	case *registerWebhook:
		if err := registerWebhookCmd(context.Background(), b, cfg, logger); err != nil {
			logger.Error("webhook registration failed", "error", err)
			os.Exit(1)
		}
		logger.Info("webhook registered", "url", cfg.TelegramWebhookURL)
		return
	case *deleteWebhook:
		if _, err := b.DeleteWebhook(context.Background(), &bot.DeleteWebhookParams{
			DropPendingUpdates: true,
		}); err != nil {
			logger.Error("webhook deletion failed", "error", err)
			os.Exit(1)
		}
		logger.Info("webhook deleted")
		return
	}

	if err := run(b, cfg, logger); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// run starts all components and blocks until shutdown.
func run(b *bot.Bot, cfg *config.Config, logger *slog.Logger) error {
	if cfg.WebhookPath() == "" {
		return errors.New("TELEGRAM_WEBHOOK_URL must contain a path (e.g. https://host/telegram/webhook)")
	}

	allow := auth.New(cfg.TelegramAllowedUserIDs)
	logger.Info("authorization loaded", "allowed_users", allow.Size())

	sessions, err := session.Open(cfg.StateFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	q := queue.New(32, logger)
	go q.Run(ctx)

	// Restore the last active Pi session after each process start.
	onStart := func(ctx context.Context, client *pi.Client) {
		st, err := client.State(ctx)
		if err != nil {
			logger.Warn("pi state query after start failed", "error", err)
			return
		}
		last := sessions.CurrentSessionFile()
		if last != "" && last != st.SessionFile {
			if err := client.SwitchSession(ctx, last); err != nil {
				logger.Warn("failed to restore pi session", "session", last, "error", err)
				return
			}
			logger.Info("restored pi session", "session", last)
		}
		if st.SessionFile != "" {
			if err := sessions.SetCurrent(st.SessionFile); err != nil {
				logger.Warn("failed to persist session state", "error", err)
			}
		}
	}

	manager := pi.NewManager(pi.ManagerOptions{
		Binary:  cfg.PIBinary,
		Args:    cfg.PIArgs(),
		Env:     cfg.ChildEnv(),
		Dir:     cfg.PIWorkingDirectory,
		Logger:  logger,
		OnStart: onStart,
	})
	go func() {
		if err := manager.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("pi manager stopped", "error", err)
		}
	}()

	sender := telegram.NewSender(b, logger)
	handler := telegram.NewHandler(sender, allow, manager, sessions, q, cfg, logger)

	webhook := telegram.NewWebhook(telegram.WebhookConfig{
		Secret:   cfg.TelegramWebhookSecret,
		Logger:   logger,
		OnUpdate: handler.HandleUpdate,
	})

	mux := http.NewServeMux()
	mux.Handle(cfg.WebhookPath(), webhook)
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              cfg.HTTPListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("http server listening", "address", cfg.HTTPListenAddress, "webhook_path", cfg.WebhookPath())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown incomplete", "error", err)
	}

	stopCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := manager.Stop(stopCtx); err != nil {
		logger.Warn("pi manager stop incomplete", "error", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// registerWebhookCmd registers the configured webhook URL with
// Telegram, including the secret token.
func registerWebhookCmd(ctx context.Context, b *bot.Bot, cfg *config.Config, logger *slog.Logger) error {
	ok, err := b.SetWebhook(ctx, &bot.SetWebhookParams{
		URL:                cfg.TelegramWebhookURL,
		SecretToken:        cfg.TelegramWebhookSecret,
		AllowedUpdates:     []string{"message"},
		DropPendingUpdates: true,
	})
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("telegram rejected setWebhook")
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return err
	}
	logger.Info("bot identity confirmed", "bot_username", me.Username)
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
