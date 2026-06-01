// Package sched runs recurring daily heat windows for the tub.
//
// The Airjet firmware only offers one-shot countdown timers (appm/timer), so it
// can't repeat a schedule day after day. This scheduler fills that gap: it holds
// a list of daily windows and, on a ticker, drives the tub's power bits on at
// each window's start and off at its stop — clearing the device's own one-shot
// timers so they don't interfere.
package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sublog.org/tubctl/internal/tub"
)

// Mode selects what stops at a window's end.
const (
	ModeHeat = "heat" // stop the heater, keep the filter pump circulating
	ModeAll  = "all"  // stop the heater and the filter pump
)

// maxSchedules bounds how many windows a client can store, keeping schedules.json
// and per-tick work from growing without limit.
const maxSchedules = 50

// Schedule is one recurring daily heat window.
type Schedule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"` // "HH:MM", 24h, local time
	Stop    string `json:"stop"`  // "HH:MM"; if <= Start the window crosses midnight
	Mode    string `json:"mode"`  // ModeHeat | ModeAll
}

// Tub is the slice of *tub.Client the scheduler needs. Defined as an interface
// so the tick logic can be tested without a real device.
type Tub interface {
	Connect(ctx context.Context) error
	Read(ctx context.Context) (*tub.Status, error)
	Write(ctx context.Context, updates map[string]any, current *tub.Status) error
}

// Scheduler owns the schedule list and applies it on a ticker.
type Scheduler struct {
	path string
	tub  Tub
	log  *slog.Logger

	mu        sync.Mutex
	schedules []Schedule
	// inWindow remembers each schedule's last-evaluated membership so we only
	// write on transitions; nil entry means "not yet evaluated" (forces a
	// reconcile on the next tick, e.g. after startup or an edit).
	inWindow map[string]bool
}

// New builds a Scheduler and loads any persisted schedules from path.
// A load error (missing/corrupt file) is logged and treated as an empty list.
func New(path string, t Tub, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{path: path, tub: t, log: log, inWindow: map[string]bool{}}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		log.Warn("schedules load failed; starting empty", "path", path, "err", err)
	}
	return s
}

// List returns a copy of the current schedules.
func (s *Scheduler) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Schedule, len(s.schedules))
	copy(out, s.schedules)
	return out
}

// Replace validates and stores a new schedule list, persists it, and resets the
// transition tracking so the next tick reconciles against the new windows.
func (s *Scheduler) Replace(list []Schedule) ([]Schedule, error) {
	if len(list) > maxSchedules {
		return nil, fmt.Errorf("too many schedules: %d (max %d)", len(list), maxSchedules)
	}
	cleaned := make([]Schedule, 0, len(list))
	for i, sc := range list {
		if _, ok := parseHHMM(sc.Start); !ok {
			return nil, fmt.Errorf("schedule %d: bad start time %q", i, sc.Start)
		}
		if _, ok := parseHHMM(sc.Stop); !ok {
			return nil, fmt.Errorf("schedule %d: bad stop time %q", i, sc.Stop)
		}
		if sc.Mode != ModeHeat && sc.Mode != ModeAll {
			return nil, fmt.Errorf("schedule %d: bad mode %q (want %q or %q)", i, sc.Mode, ModeHeat, ModeAll)
		}
		// Always assign the ID server-side. Trusting client IDs lets a caller
		// send duplicates, which collide in the inWindow map and silently break
		// edge detection for the masked schedule.
		sc.ID = fmt.Sprintf("s%d-%d", time.Now().UnixNano(), i)
		cleaned = append(cleaned, sc)
	}

	s.mu.Lock()
	s.schedules = cleaned
	s.inWindow = map[string]bool{} // force reconcile on next tick
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.log.Warn("schedules save failed", "path", s.path, "err", err)
		return cleaned, fmt.Errorf("persisting schedules: %w", err)
	}
	return cleaned, nil
}

// Run drives the scheduler until ctx is cancelled. Checking every 30s is plenty:
// transitions are detected by window membership, not exact-minute matching, so a
// coarse tick can't miss an edge.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	s.tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.tick(ctx, now)
		}
	}
}

// tick evaluates every enabled schedule and applies on/off actions on transitions.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	nowMin := now.Hour()*60 + now.Minute()

	s.mu.Lock()
	schedules := make([]Schedule, len(s.schedules))
	copy(schedules, s.schedules)
	s.mu.Unlock()

	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		start, ok1 := parseHHMM(sc.Start)
		stop, ok2 := parseHHMM(sc.Stop)
		if !ok1 || !ok2 {
			continue
		}
		cur := inDailyWindow(nowMin, start, stop)

		s.mu.Lock()
		prev, seen := s.inWindow[sc.ID]
		s.mu.Unlock()

		// Decide whether this tick needs a write. We only commit the new
		// membership once the write lands, so a transient device outage at an
		// edge is retried on the next tick instead of being silently skipped.
		applied := true
		switch {
		case !seen && cur:
			// First evaluation inside a window (startup/after edit): reconcile to
			// ON. We never force-off here — that would stomp a manual session
			// started outside any window.
			applied = s.applyOn(ctx, sc)
		case seen && cur && !prev:
			applied = s.applyOn(ctx, sc)
		case seen && !cur && prev:
			applied = s.applyOff(ctx, sc)
		}

		if applied {
			s.mu.Lock()
			s.inWindow[sc.ID] = cur
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) applyOn(ctx context.Context, sc Schedule) bool {
	return s.apply(ctx, sc, "start", map[string]any{
		"heat_power": 1, "filter_power": 1,
		// Clear the device's one-shot timers so they don't fight the scheduler.
		"heat_appm_min": 0, "heat_timer_min": 0,
		"filter_appm_min": 0, "filter_timer_min": 0,
	})
}

func (s *Scheduler) applyOff(ctx context.Context, sc Schedule) bool {
	updates := map[string]any{"heat_power": 0, "filter_power": 1}
	if sc.Mode == ModeAll {
		updates["filter_power"] = 0
	}
	return s.apply(ctx, sc, "stop", updates)
}

// apply pushes updates to the tub, returning true on success. A false return
// leaves the schedule's membership uncommitted so the edge is retried.
func (s *Scheduler) apply(ctx context.Context, sc Schedule, edge string, updates map[string]any) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.tub.Connect(ctx); err != nil {
		s.log.Warn("schedule connect failed", "id", sc.ID, "edge", edge, "err", err)
		return false
	}
	cur, err := s.tub.Read(ctx)
	if err != nil {
		s.log.Warn("schedule pre-read failed", "id", sc.ID, "edge", edge, "err", err)
		return false
	}
	if err := s.tub.Write(ctx, updates, cur); err != nil {
		s.log.Warn("schedule write failed", "id", sc.ID, "edge", edge, "err", err)
		return false
	}
	s.log.Info("schedule applied", "id", sc.ID, "edge", edge, "mode", sc.Mode, "updates", updates)
	return true
}

func (s *Scheduler) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var list []Schedule
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	s.mu.Lock()
	s.schedules = list
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.schedules, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	// Atomic replace: write a temp file in the same dir, then rename.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// parseHHMM parses "HH:MM" (24h) into minutes-of-day [0,1440).
func parseHHMM(v string) (int, bool) {
	var h, m int
	n, err := fmt.Sscanf(v, "%d:%d", &h, &m)
	if err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// inDailyWindow reports whether nowMin falls in [start, stop), handling windows
// that cross midnight (start >= stop). A zero-length window is never active.
func inDailyWindow(nowMin, start, stop int) bool {
	if start == stop {
		return false
	}
	if start < stop {
		return nowMin >= start && nowMin < stop
	}
	return nowMin >= start || nowMin < stop
}
