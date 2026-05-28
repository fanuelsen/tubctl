package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"sublog.org/tubctl/internal/tub"
)

// ANSI escapes — degrade gracefully when stdout isn't a terminal.
const (
	cReset = "\x1b[0m"
	cBold  = "\x1b[1m"
	cDim   = "\x1b[2m"
	cRed   = "\x1b[31m"
	cYel   = "\x1b[33m"
	cGrn   = "\x1b[32m"
)

func color(c, s string) string {
	if !isTerm(os.Stdout) {
		return s
	}
	return c + s + cReset
}

func isTerm(f *os.File) bool {
	// best-effort: check if stdout is a character device
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func onOff(b bool) string {
	if b {
		return color(cGrn, "ON")
	}
	return color(cDim, "off")
}

func runState(_ []string) {
	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil)) // silent for one-shot CLI
	tubClient := tub.NewClient(cfg.TubHost, cfg.TubPort, log)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := tubClient.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s connecting to %s:%d: %v\n", color(cRed, "error"), cfg.TubHost, cfg.TubPort, err)
		os.Exit(1)
	}
	defer tubClient.Close()

	s, err := tubClient.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s reading state: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}
	printState(os.Stdout, s, cfg.TubHost)
}

func printState(w io.Writer, s *tub.Status, host string) {
	tempLine := color(cBold, fmt.Sprintf("%d°%s", s.TempNow, s.TempUnit))
	if s.HeatTempReach {
		tempLine = color(cGrn, tempLine)
	} else if s.HeatPower {
		tempLine = color(cYel, tempLine)
	} else {
		tempLine = color(cDim, tempLine)
	}

	heaterNote := ""
	if s.HeatTempReach {
		heaterNote = color(cGrn, "(at target)")
	} else if s.HeatPower {
		heaterNote = color(cYel, "(heating)")
	}

	fmt.Fprintf(w, `
%s
  power       %s
  heater      %s    %s
  filter      %s
  bubbles     %s
  locked      %s
  earth       %s

  current     %s
  target      %d°%s

  timers (min): heat_in=%d heat_for=%d  filter_in=%d filter_for=%d  wave_in=%d wave_for=%d
`,
		color(cBold, fmt.Sprintf("Tub @ %s", host)),
		onOff(s.Power),
		onOff(s.HeatPower), heaterNote,
		onOff(s.FilterPower),
		onOff(s.WavePower),
		onOff(s.Locked),
		onOff(s.Earth),
		tempLine,
		s.TempSet, s.TempUnit,
		s.HeatAppmMin, s.HeatTimerMin,
		s.FilterAppmMin, s.FilterTimerMin,
		s.WaveAppmMin, s.WaveTimerMin,
	)
	if len(s.Errors) > 0 {
		fmt.Fprintf(w, "  errors      %s\n", color(cRed, fmt.Sprintf("%v", s.Errors)))
	}
	fmt.Fprintln(w)
}
