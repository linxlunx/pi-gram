package config

import (
	"os"
	"testing"
	"time"
)

func setenv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadAndValidate(t *testing.T) {
	setenv(t, map[string]string{
		"TELEGRAM_BOT_TOKEN":        "123:abc",
		"TELEGRAM_WEBHOOK_SECRET":   "sekrit",
		"TELEGRAM_WEBHOOK_URL":      "https://bot.example.com/telegram/webhook",
		"TELEGRAM_ALLOWED_USER_IDS": "111, 222,9999999999",
		"PI_MODEL":                  "opencode/gpt-x",
		"PI_BINARY":                 "/usr/local/bin/pi",
		"PI_SESSION_DIRECTORY":      t.TempDir(),
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.TelegramBotToken != "123:abc" || cfg.TelegramWebhookSecret != "sekrit" {
		t.Fatalf("unexpected telegram config: %+v", cfg)
	}
	wantIDs := []int64{111, 222, 9999999999}
	if len(cfg.TelegramAllowedUserIDs) != len(wantIDs) {
		t.Fatalf("allowed ids = %v, want %v", cfg.TelegramAllowedUserIDs, wantIDs)
	}
	for i, id := range cfg.TelegramAllowedUserIDs {
		if id != wantIDs[i] {
			t.Fatalf("allowed ids = %v, want %v", cfg.TelegramAllowedUserIDs, wantIDs)
		}
	}
	if cfg.PIBinary != "/usr/local/bin/pi" {
		t.Fatalf("binary = %q", cfg.PIBinary)
	}
	if cfg.WebhookPath() != "/telegram/webhook" {
		t.Fatalf("webhook path = %q", cfg.WebhookPath())
	}
	if cfg.PromptTimeout != 15*time.Minute {
		t.Fatalf("default prompt timeout = %v", cfg.PromptTimeout)
	}
}

func TestValidateMissing(t *testing.T) {
	setenv(t, map[string]string{
		"TELEGRAM_BOT_TOKEN":      "",
		"TELEGRAM_WEBHOOK_SECRET": "",
		"TELEGRAM_WEBHOOK_URL":    "",
		"PI_MODEL":                "",
	})
	// Clear any leftover allowed IDs from the environment.
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_WEBHOOK_URL", "TELEGRAM_ALLOWED_USER_IDS", "PI_MODEL"} {
		if !contains(err.Error(), key) {
			t.Errorf("error should mention %s: %v", key, err)
		}
	}
}

func TestPIArgs(t *testing.T) {
	setenv(t, map[string]string{
		"PI_PROVIDER":               "opencode",
		"PI_MODEL":                  "opencode/gpt-x",
		"PI_SESSION_DIRECTORY":      "/var/lib/pi/sessions",
		"TELEGRAM_BOT_TOKEN":        "x",
		"TELEGRAM_WEBHOOK_SECRET":   "x",
		"TELEGRAM_WEBHOOK_URL":      "https://h/p",
		"TELEGRAM_ALLOWED_USER_IDS": "1",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := cfg.PIArgs()
	want := []string{"--mode", "rpc", "--provider", "opencode", "--model", "opencode/gpt-x", "--session-dir", "/var/lib/pi/sessions"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestChildEnvInjectsAPIKey(t *testing.T) {
	setenv(t, map[string]string{
		"OPENCODE_API_KEY": "sk-secret",
	})
	cfg := &Config{OpenCodeAPIKey: "sk-secret"}
	env := cfg.ChildEnv()
	found := false
	for _, kv := range env {
		if kv == "OPENCODE_API_KEY=sk-secret" {
			found = true
		}
	}
	if !found {
		t.Fatal("API key not injected into child env")
	}
}

func TestParseUserIDsIgnoresGarbage(t *testing.T) {
	ids := parseUserIDs("1, not-a-number, ,2, 3.5")
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.env"
	content := "# comment\n" +
		"\n" +
		"TEST_ENV_A=plain\n" +
		"TEST_ENV_B=\"quoted value\"\n" +
		"export TEST_ENV_C='single quoted'\n" +
		"TEST_ENV_D\n" + // malformed, ignored
		"TEST_ENV_E=with=equals\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	for key, want := range map[string]string{
		"TEST_ENV_A": "plain",
		"TEST_ENV_B": "quoted value",
		"TEST_ENV_C": "single quoted",
		"TEST_ENV_E": "with=equals",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := os.Getenv("TEST_ENV_D"); got != "" {
		t.Errorf("malformed line should be ignored, TEST_ENV_D = %q", got)
	}
}

func TestLoadEnvFileDoesNotOverrideExistingEnv(t *testing.T) {
	t.Setenv("TEST_ENV_EXISTING", "from-env")
	dir := t.TempDir()
	path := dir + "/test.env"
	if err := os.WriteFile(path, []byte("TEST_ENV_EXISTING=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got := os.Getenv("TEST_ENV_EXISTING"); got != "from-env" {
		t.Fatalf("existing env was overridden: %q", got)
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	if err := LoadEnvFile("/does/not/exist.env"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
