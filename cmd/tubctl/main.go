// tubctl — self-hosted LAN-only webapp for Bestway Airjet hot tubs.
//
// Speaks the Gizwits GAgent LAN protocol directly to the tub on the LAN;
// no cloud, no Bestway account.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sublog.org/tubctl/internal/tub"
	"sublog.org/tubctl/internal/web"
)

func main() {
	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	tubClient := tub.NewClient(cfg.TubHost, cfg.TubPort, log)

	srv, err := web.New(tubClient, cfg.TimeFormat, log)
	if err != nil {
		log.Error("init server", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.HTTPPort),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// initial connect (non-fatal if tub is offline; handlers will retry)
	connCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tubClient.Connect(connCtx); err != nil {
		log.Warn("initial tub connect failed (will retry on demand)", "err", err)
	}
	cancel()

	log.Info("tubctl listening",
		"addr", httpServer.Addr, "tub", hostPort{cfg.TubHost, cfg.TubPort}, "time_format", cfg.TimeFormat)

	// graceful shutdown on SIGINT/SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		_ = tubClient.Close()
	}()

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http serve", "err", err)
		os.Exit(1)
	}
}

// hostPort is a tiny helper for structured logging of host+port.
type hostPort struct {
	Host string
	Port int
}

func (h hostPort) LogValue() slog.Value {
	return slog.StringValue(h.Host + ":" + strconv.Itoa(h.Port))
}

type config struct {
	TubHost    string
	TubPort    int
	HTTPPort   int
	TimeFormat string
	LogLevel   slog.Level
}

func loadConfig() config {
	c := config{
		TubHost:    envStr("TUB_HOST", "172.31.0.105"),
		TubPort:    envInt("TUB_PORT", 12416),
		HTTPPort:   envInt("PORT", 3000),
		TimeFormat: envStr("TIME_FORMAT", "24"),
		LogLevel:   slog.LevelInfo,
	}
	if c.TimeFormat != "12" {
		c.TimeFormat = "24"
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		switch v {
		case "debug": c.LogLevel = slog.LevelDebug
		case "warn":  c.LogLevel = slog.LevelWarn
		case "error": c.LogLevel = slog.LevelError
		}
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
