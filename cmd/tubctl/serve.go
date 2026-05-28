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

func runServe(_ []string) {
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

	connCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tubClient.Connect(connCtx); err != nil {
		log.Warn("initial tub connect failed (will retry on demand)", "err", err)
	}
	cancel()

	log.Info("tubctl listening",
		"addr", httpServer.Addr, "tub", hostPort{cfg.TubHost, cfg.TubPort}, "time_format", cfg.TimeFormat)

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
