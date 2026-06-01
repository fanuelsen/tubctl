package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	"sublog.org/tubctl/internal/tub"
)

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	interval := fs.Duration("interval", 2*time.Second, "polling interval")
	fs.Parse(args)

	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tubClient := tub.NewClient(cfg.TubHost, cfg.TubPort, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	connCtx, cc := context.WithTimeout(ctx, 8*time.Second)
	if err := tubClient.Connect(connCtx); err != nil {
		cc()
		fmt.Fprintf(os.Stderr, "%s connecting: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}
	cc()
	defer tubClient.Close()

	fmt.Printf("%s @ %s:%d  (interval %s, Ctrl-C to stop)\n\n",
		color(cBold, "watching"), cfg.TubHost, cfg.TubPort, *interval)

	t := time.NewTicker(*interval)
	defer t.Stop()

	var prev map[string]any
	tick := func() {
		readCtx, rc := context.WithTimeout(ctx, 4*time.Second)
		defer rc()
		s, err := tubClient.Read(readCtx)
		if err != nil {
			fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), color(cRed, "read error: "+err.Error()))
			return
		}
		curr := s.Map()
		now := time.Now().Format("15:04:05")
		if prev == nil {
			fmt.Printf("%s  %s\n", color(cDim, now), fmtState(curr))
		} else {
			diff := diffMap(prev, curr)
			if len(diff) == 0 {
				return // skip ticks with no changes
			}
			fmt.Printf("%s  %s\n", color(cDim, now), fmtChanges(prev, curr, diff))
		}
		prev = curr
	}
	tick()
	for {
		select {
		case <-ctx.Done():
			fmt.Println(color(cDim, "\n[stopped]"))
			return
		case <-t.C:
			tick()
		}
	}
}

func diffMap(a, b map[string]any) map[string]bool {
	out := map[string]bool{}
	for k, v := range b {
		if !reflect.DeepEqual(a[k], v) {
			out[k] = true
		}
	}
	return out
}

func fmtState(m map[string]any) string {
	return fmt.Sprintf("power=%v heat=%v filter=%v wave=%v locked=%v  now=%d set=%d",
		m["power"], m["heat_power"], m["filter_power"], m["wave_power"], m["locked"],
		m["temp_set"], m["temp_set"])
}

func fmtChanges(prev, curr map[string]any, diff map[string]bool) string {
	parts := []string{}
	for k := range diff {
		parts = append(parts, fmt.Sprintf("%s %v→%v", color(cBold, k), prev[k], curr[k]))
	}
	return joinSpace(parts)
}

func joinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "  "
		}
		out += p
	}
	return out
}
