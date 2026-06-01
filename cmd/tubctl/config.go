package main

import (
	"log/slog"
	"os"
	"strconv"
)

type config struct {
	TubHost    string
	TubPort    int
	HTTPPort   int
	TimeFormat string
	DataDir    string
	LogLevel   slog.Level
}

func loadConfig() config {
	c := config{
		TubHost:    envStr("TUB_HOST", "172.31.0.105"),
		TubPort:    envInt("TUB_PORT", 12416),
		HTTPPort:   envInt("PORT", 3000),
		TimeFormat: envStr("TIME_FORMAT", "24"),
		DataDir:    envStr("DATA_DIR", "data"),
		LogLevel:   slog.LevelInfo,
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
