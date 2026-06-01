package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"sublog.org/tubctl/internal/tub"
)

func runSet(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tubctl set key=value [key=value ...]")
		fmt.Fprintln(os.Stderr, "writable:", writableNames())
		os.Exit(2)
	}
	updates, err := parseSetArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", color(cRed, "error:"), err)
		os.Exit(2)
	}

	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tubClient := tub.NewClient(cfg.TubHost, cfg.TubPort, log)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tubClient.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s connecting: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}
	defer tubClient.Close()

	before, err := tubClient.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s reading state: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}

	fmt.Printf("%s %v\n", color(cYel, "writing:"), updates)
	if err := tubClient.Write(ctx, updates, before); err != nil {
		fmt.Fprintf(os.Stderr, "%s writing: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}

	time.Sleep(500 * time.Millisecond)
	after, err := tubClient.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s re-reading: %v\n", color(cRed, "error"), err)
		os.Exit(1)
	}

	// Verify each requested change took effect.
	ok := true
	beforeMap := before.Map()
	afterMap := after.Map()
	for k, want := range updates {
		got := afterMap[k]
		oldv := beforeMap[k]
		if fmt.Sprintf("%v", got) == fmt.Sprintf("%v", normalize(k, want)) {
			fmt.Printf("  %s %s: %v → %v\n", color(cGrn, "✓"), k, oldv, got)
		} else {
			fmt.Printf("  %s %s: wanted %v, got %v (was %v)\n", color(cRed, "✗"), k, want, got, oldv)
			ok = false
		}
	}
	if !ok {
		os.Exit(1)
	}
}

// parseSetArgs turns ["heat_power=1", "temp_set=38"] into a typed update map.
func parseSetArgs(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad arg %q (expected key=value)", a)
		}
		attr, known := findAttr(k)
		if !known {
			return nil, fmt.Errorf("unknown writable %q. valid: %s", k, writableNames())
		}
		switch attr.Kind {
		case "bool", "enum":
			out[k] = parseBool(v)
		case "uint8":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 255 {
				return nil, fmt.Errorf("%s: expected uint8 (0-255), got %q", k, v)
			}
			out[k] = n
		case "uint16":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return nil, fmt.Errorf("%s: expected uint16 (0-65535), got %q", k, v)
			}
			out[k] = n
		}
	}
	return out, nil
}

func parseBool(v string) int {
	switch strings.ToLower(v) {
	case "1", "true", "on", "yes":
		return 1
	}
	return 0
}

// normalize converts the input value (any) to the canonical type returned by
// the device, so the verification compare matches (bool vs int, etc.).
func normalize(k string, v any) any {
	attr, _ := findAttr(k)
	switch attr.Kind {
	case "bool":
		switch x := v.(type) {
		case bool:
			return x
		case int:
			return x != 0
		case float64:
			return x != 0
		}
		return false
	case "enum", "uint8", "uint16":
		switch x := v.(type) {
		case int:
			return x
		case float64:
			return int(x)
		}
		return v
	}
	return v
}

func findAttr(name string) (tub.WritableAttr, bool) {
	for _, a := range tub.Writable {
		if a.Name == name {
			return a, true
		}
	}
	return tub.WritableAttr{}, false
}

func writableNames() string {
	names := make([]string, 0, len(tub.Writable))
	for _, a := range tub.Writable {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
