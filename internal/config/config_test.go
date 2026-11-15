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
		"PI_MODEL":                  "prov/model-x",
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
		"PI_PROVIDER":             "",
		"PI_MODEL":                "",
	})
	// Clear any leftover allowed IDs from the environment.
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error")
	}
	// Provider and model are optional; only Telegram values are required.
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_WEBHOOK_URL", "TELEGRAM_ALLOWED_USER_IDS"} {
		if !contains(err.Error(), key) {
			t.Errorf("error should mention %s: %v", key, err)
		}
	}
}

func TestPIArgs(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "provider and model set",
			cfg:  Config{PIProvider: "prov", PIModel: "prov/model-x", PISessionDirectory: "/var/lib/pi/sessions"},
			want: []string{"--mode", "rpc", "--provider", "prov", "--model", "prov/model-x", "--session-dir", "/var/lib/pi/sessions"},
		},
		{
			name: "provider only",
			cfg:  Config{PIProvider: "prov", PISessionDirectory: "/var/lib/pi/sessions"},
			want: []string{"--mode", "rpc", "--provider", "prov", "--session-dir", "/var/lib/pi/sessions"},
		},
		{
			name: "neither set, pi defaults apply",
			cfg:  Config{PISessionDirectory: "/var/lib/pi/sessions"},
			want: []string{"--mode", "rpc", "--session-dir", "/var/lib/pi/sessions"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.PIArgs()
			if len(got) != len(tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("args = %v, want %v", got, tc.want)
				}
			}
		})
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
