package telegram

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
)

// secretHeader is the header Telegram sets on webhook requests.
const secretHeader = "X-Telegram-Bot-Api-Secret-Token"

// maxBodySize caps webhook request bodies (Telegram updates are small).
const maxBodySize = 1 << 20

// UpdateTracker de-duplicates Telegram updates by update_id. Telegram
// may redeliver updates if it did not receive a timely 200 response.
type UpdateTracker struct {
	mu      sync.Mutex
	seen    map[int64]time.Time
	maxSize int
	ttl     time.Duration
}

// NewUpdateTracker creates a tracker keeping at most maxSize recent
// update IDs for ttl.
func NewUpdateTracker(maxSize int, ttl time.Duration) *UpdateTracker {
	if maxSize <= 0 {
		maxSize = 2048
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &UpdateTracker{seen: make(map[int64]time.Time), maxSize: maxSize, ttl: ttl}
}

// First reports whether the update ID was not seen before and records
// it. It returns false for duplicates.
func (t *UpdateTracker) First(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[id]; ok {
		return false
	}
	t.seen[id] = time.Now()
	if len(t.seen) > t.maxSize {
		t.pruneLocked()
	}
	return true
}

func (t *UpdateTracker) pruneLocked() {
	cutoff := time.Now().Add(-t.ttl)
	for id, ts := range t.seen {
		if ts.Before(cutoff) {
			delete(t.seen, id)
		}
	}
	// If still oversized (e.g. burst), drop the oldest entries.
	for len(t.seen) > t.maxSize {
		var oldestID int64
		var oldest time.Time
		first := true
		for id, ts := range t.seen {
			if first || ts.Before(oldest) {
				oldestID, oldest, first = id, ts, false
			}
		}
		delete(t.seen, oldestID)
	}
}

// WebhookConfig configures the webhook endpoint.
type WebhookConfig struct {
	// Secret is the value Telegram sends in the secret token header.
	Secret string
	// Logger receives webhook events.
	Logger *slog.Logger
	// OnUpdate is invoked asynchronously for each accepted update.
	OnUpdate func(u *models.Update)
}

// Webhook is the http.Handler for Telegram's webhook API. It validates
// the request method and secret token, parses the update, answers 200
// immediately, and processes updates asynchronously.
type Webhook struct {
	cfg    WebhookConfig
	logger *slog.Logger
	seen   *UpdateTracker
}

// NewWebhook creates the webhook endpoint handler.
func NewWebhook(cfg WebhookConfig) *Webhook {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Webhook{cfg: cfg, logger: cfg.Logger, seen: NewUpdateTracker(0, 0)}
}

// ServeHTTP implements http.Handler.
func (w *Webhook) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.logger.Warn("webhook rejected non-POST request", "method", req.Method, "remote", req.RemoteAddr)
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if w.cfg.Secret != "" && req.Header.Get(secretHeader) != w.cfg.Secret {
		w.logger.Warn("webhook rejected request with invalid secret token", "remote", req.RemoteAddr)
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodySize+1))
	if err != nil {
		w.logger.Warn("webhook failed to read body", "error", err)
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > maxBodySize {
		w.logger.Warn("webhook body too large")
		http.Error(rw, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	var update models.Update
	if err := json.Unmarshal(body, &update); err != nil {
		w.logger.Warn("webhook failed to parse update", "error", err)
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if !w.seen.First(update.ID) {
		w.logger.Debug("duplicate update dropped", "update_id", update.ID)
		rw.WriteHeader(http.StatusOK)
		return
	}
	// Acknowledge immediately; process asynchronously.
	rw.WriteHeader(http.StatusOK)
	if w.cfg.OnUpdate != nil {
		go w.cfg.OnUpdate(&update)
	}
}
