// Package config loads and validates application configuration from
// environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application settings.
type Config struct {
	// Telegram settings.
	TelegramBotToken       string
	TelegramWebhookSecret  string
	TelegramWebhookURL     string
	TelegramAllowedUserIDs []int64

	// Pi process settings.
	PIBinary           string
	PIProvider         string
	PIModel            string
	PIWorkingDirectory string
	PISessionDirectory string

	// HTTP server settings.
	HTTPListenAddress string
	LogLevel          string

	// Internal behavior.
	PromptTimeout  time.Duration
	BashTimeout    time.Duration
	CommandTimeout time.Duration
	StateFile      string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:  os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		TelegramWebhookURL:     os.Getenv("TELEGRAM_WEBHOOK_URL"),
		TelegramAllowedUserIDs: parseUserIDs(os.Getenv("TELEGRAM_ALLOWED_USER_IDS")),

		PIBinary:           getenv("PI_BINARY", "pi"),
		PIProvider:         os.Getenv("PI_PROVIDER"),
		PIModel:            os.Getenv("PI_MODEL"),
		PIWorkingDirectory: os.Getenv("PI_WORKING_DIRECTORY"),
		PISessionDirectory: getenv("PI_SESSION_DIRECTORY", "/var/lib/pi/sessions"),

		HTTPListenAddress: getenv("HTTP_LISTEN_ADDRESS", "127.0.0.1:8080"),
		LogLevel:          getenv("LOG_LEVEL", "info"),

		PromptTimeout:  durationEnv("PI_PROMPT_TIMEOUT_SECONDS", 15*time.Minute),
		BashTimeout:    durationEnv("PI_BASH_TIMEOUT_SECONDS", 5*time.Minute),
		CommandTimeout: durationEnv("PI_COMMAND_TIMEOUT_SECONDS", 60*time.Second),

		StateFile: os.Getenv("STATE_FILE"),
	}
	if cfg.StateFile == "" {
		cfg.StateFile = cfg.PISessionDirectory + "/pi-telegram-state.json"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks that required configuration values are present.
// Provider and model are deliberately not validated: they are passed
// through to Pi, which is authoritative for its own defaults.
func (c *Config) Validate() error {
	var missing []string
	if c.TelegramBotToken == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}
	if c.TelegramWebhookSecret == "" {
		missing = append(missing, "TELEGRAM_WEBHOOK_SECRET")
	}
	if c.TelegramWebhookURL == "" {
		missing = append(missing, "TELEGRAM_WEBHOOK_URL")
	}
	if len(c.TelegramAllowedUserIDs) == 0 {
		missing = append(missing, "TELEGRAM_ALLOWED_USER_IDS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}

// WebhookPath returns the URL path the webhook should be served on,
// derived from the configured public webhook URL.
func (c *Config) WebhookPath() string {
	u := c.TelegramWebhookURL
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "/"); i >= 0 {
		return strings.TrimSuffix(u[i:], "/")
	}
	return ""
}

// PIArgs builds the command line arguments used to start Pi in RPC
// mode. Provider and model flags are only included when configured;
// otherwise Pi uses its own defaults.
func (c *Config) PIArgs() []string {
	args := []string{"--mode", "rpc"}
	if c.PIProvider != "" {
		args = append(args, "--provider", c.PIProvider)
	}
	if c.PIModel != "" {
		args = append(args, "--model", c.PIModel)
	}
	args = append(args, "--session-dir", c.PISessionDirectory)
	return args
}

// LoadEnvFile reads KEY=VALUE pairs from a file into the process
// environment. Lines starting with '#' and blank lines are ignored.
// Values may be single- or double-quoted. Existing environment
// variables take precedence, so real env wins over the file.
func LoadEnvFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if ln := len(val); ln >= 2 &&
			((val[0] == '"' && val[ln-1] == '"') || (val[0] == '\'' && val[ln-1] == '\'')) {
			val = val[1 : ln-1]
		}
		if _, ok := os.LookupEnv(key); !ok {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}
	}
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseUserIDs(s string) []int64 {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func durationEnv(key string, def time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	secs, err := strconv.Atoi(s)
	if err != nil || secs <= 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}
