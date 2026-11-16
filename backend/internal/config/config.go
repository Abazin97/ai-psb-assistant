package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port         string
	DatabaseURL  string
	MLURL        string
	MLMock       bool
	MLTimeout    time.Duration
	HistoryLimit int
	MaxTextChars int
	MaxBodyBytes int64
	ChatTTL      time.Duration
	CookieSecure bool
}

func Load() (Config, error) {
	c := Config{
		Port:         env("PORT", "8080"),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		MLURL:        env("ML_URL", "http://ml:8000"),
		MLTimeout:    150 * time.Second,
		HistoryLimit: 8,
		MaxTextChars: 4000,
		MaxBodyBytes: 1 << 20,
		ChatTTL:      24 * time.Hour,
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	var err error
	if c.MLTimeout, err = durationEnv("ML_TIMEOUT", c.MLTimeout); err != nil {
		return Config{}, err
	}
	if c.ChatTTL, err = durationEnv("CHAT_TTL", c.ChatTTL); err != nil {
		return Config{}, err
	}
	if c.HistoryLimit, err = intEnv("HISTORY_LIMIT", c.HistoryLimit); err != nil {
		return Config{}, err
	}
	if c.MaxTextChars, err = intEnv("MAX_TEXT_CHARS", c.MaxTextChars); err != nil {
		return Config{}, err
	}
	if c.MaxBodyBytes, err = int64Env("MAX_BODY_BYTES", c.MaxBodyBytes); err != nil {
		return Config{}, err
	}
	if c.CookieSecure, err = boolEnv("COOKIE_SECURE", false); err != nil {
		return Config{}, err
	}
	if c.MLMock, err = boolEnv("ML_MOCK", false); err != nil {
		return Config{}, err
	}
	port, portErr := strconv.Atoi(c.Port)
	if portErr != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("PORT must be a valid TCP port")
	}
	if c.MLTimeout <= 0 || c.ChatTTL <= 0 || c.HistoryLimit < 0 || c.MaxTextChars <= 0 || c.MaxBodyBytes <= 0 {
		return Config{}, fmt.Errorf("duration and size settings must be positive (HISTORY_LIMIT may be zero)")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func intEnv(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func int64Env(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
