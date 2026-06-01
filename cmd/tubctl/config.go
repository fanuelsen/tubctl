package main

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
)

type config struct {
	TubHost      string
	TubPort      int
	HTTPPort     int
	TimeFormat   string
	DataDir      string
	AuthToken    string
	AllowedHosts []string
	LogLevel     slog.Level
}

func loadConfig() config {
	c := config{
		TubHost:      envStr("TUB_HOST", "172.31.0.105"),
		TubPort:      envInt("TUB_PORT", 12416),
		HTTPPort:     envInt("PORT", 3000),
		TimeFormat:   envStr("TIME_FORMAT", "24"),
		DataDir:      envStr("DATA_DIR", "data"),
		AuthToken:    os.Getenv("AUTH_TOKEN"),
		AllowedHosts: splitList(os.Getenv("ALLOWED_HOSTS")),
		LogLevel:     slog.LevelInfo,
	}
	if c.TimeFormat != "12" {
		c.TimeFormat = "24"
	}
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	}
	return c
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// hostPort is a tiny helper for structured logging of host+port.
type hostPort struct {
	Host string
	Port int
}

func (h hostPort) LogValue() slog.Value {
	return slog.StringValue(h.Host + ":" + strconv.Itoa(h.Port))
}
