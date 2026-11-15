package telegram

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func postUpdate(t *testing.T, h http.Handler, secret string, update models.Update) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(secretHeader, secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookAcceptsValidUpdate(t *testing.T) {
	updates := make(chan *models.Update, 4)
	h := NewWebhook(WebhookConfig{
		Secret:   "s3cret",
		Logger:   discardLogger(),
		OnUpdate: func(u *models.Update) { updates <- u },
	})

	rec := postUpdate(t, h, "s3cret", models.Update{ID: 1, Message: &models.Message{Text: "hi"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	select {
	case u := <-updates:
		if u.ID != 1 || u.Message.Text != "hi" {
			t.Fatalf("unexpected update: %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("update was not dispatched")
	}
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	h := NewWebhook(WebhookConfig{Secret: "s3cret", Logger: discardLogger()})
	rec := postUpdate(t, h, "wrong", models.Update{ID: 1})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestWebhookRejectsMissingSecret(t *testing.T) {
	h := NewWebhook(WebhookConfig{Secret: "s3cret", Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestWebhookRejectsNonPost(t *testing.T) {
	h := NewWebhook(WebhookConfig{Secret: "s3cret", Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(secretHeader, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestWebhookRejectsBadJSON(t *testing.T) {
	h := NewWebhook(WebhookConfig{Secret: "s3cret", Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not json")))
	req.Header.Set(secretHeader, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWebhookDuplicateUpdateHandledOnce(t *testing.T) {
	updates := make(chan *models.Update, 4)
	h := NewWebhook(WebhookConfig{
		Secret:   "s3cret",
		Logger:   discardLogger(),
		OnUpdate: func(u *models.Update) { updates <- u },
	})
	postUpdate(t, h, "s3cret", models.Update{ID: 7, Message: &models.Message{Text: "once"}})
	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("first update was not dispatched")
	}
	rec := postUpdate(t, h, "s3cret", models.Update{ID: 7, Message: &models.Message{Text: "once"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate should still be acknowledged with 200, got %d", rec.Code)
	}
	select {
	case u := <-updates:
		t.Fatalf("duplicate update dispatched: %+v", u)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUpdateTrackerPrune(t *testing.T) {
	tr := NewUpdateTracker(4, time.Hour)
	for i := int64(0); i < 10; i++ {
		if !tr.First(i) {
			t.Fatalf("update %d unexpectedly duplicate", i)
		}
	}
	// All ten were seen; the oldest four were pruned.
	if !tr.First(0) {
		t.Fatal("pruned id should be accepted again")
	}
	if tr.First(9) {
		t.Fatal("recent id must still be tracked as seen")
	}
}
