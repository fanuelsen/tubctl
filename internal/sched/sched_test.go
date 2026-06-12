package sched

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sublog.org/tubctl/internal/tub"
)

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"00:00", 0, true},
		{"09:30", 570, true},
		{"23:59", 1439, true},
		{"7:5", 425, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHHMM(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseHHMM(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestInDailyWindow(t *testing.T) {
	cases := []struct {
		name             string
		now, start, stop int
		want             bool
	}{
		{"inside normal", 11 * 60, 10 * 60, 12 * 60, true},
		{"before normal", 9 * 60, 10 * 60, 12 * 60, false},
		{"at start", 10 * 60, 10 * 60, 12 * 60, true},
		{"at stop excluded", 12 * 60, 10 * 60, 12 * 60, false},
		{"midnight wrap inside late", 23 * 60, 22 * 60, 6 * 60, true},
		{"midnight wrap inside early", 2 * 60, 22 * 60, 6 * 60, true},
		{"midnight wrap outside", 12 * 60, 22 * 60, 6 * 60, false},
		{"zero length", 60, 60, 60, false},
	}
	for _, c := range cases {
		if got := inDailyWindow(c.now, c.start, c.stop); got != c.want {
			t.Errorf("%s: inDailyWindow(%d,%d,%d) = %v, want %v", c.name, c.now, c.start, c.stop, got, c.want)
		}
	}
}

type fakeTub struct {
	writes   []map[string]any
	failNext bool // when true, the next Write fails (and resets)
}

func (f *fakeTub) Connect(context.Context) error { return nil }
func (f *fakeTub) Read(context.Context) (*tub.Status, error) {
	return &tub.Status{}, nil
}
func (f *fakeTub) Write(_ context.Context, updates map[string]any, _ *tub.Status) error {
	if f.failNext {
		f.failNext = false
		return errContext
	}
	f.writes = append(f.writes, updates)
	return nil
}

var errContext = context.DeadlineExceeded

func at(h, m int) time.Time { return time.Date(2026, 1, 2, h, m, 0, 0, time.Local) }

func TestTickTransitions(t *testing.T) {
	ft := &fakeTub{}
	s := New(filepath.Join(t.TempDir(), "schedules.json"), ft, nil)
	if _, err := s.Replace([]Schedule{
		{Enabled: true, Start: "10:00", Stop: "12:00", Mode: ModeHeat},
	}); err != nil {
		t.Fatal(err)
	}

	// First eval outside the window: no write, just records membership.
	s.tick(context.Background(), at(9, 0))
	if len(ft.writes) != 0 {
		t.Fatalf("expected no write before window, got %v", ft.writes)
	}

	// Entering the window: heater + filter on.
	s.tick(context.Background(), at(11, 0))
	if len(ft.writes) != 1 {
		t.Fatalf("expected one write on start edge, got %d", len(ft.writes))
	}
	if ft.writes[0]["heat_power"] != 1 || ft.writes[0]["filter_power"] != 1 {
		t.Errorf("start write should turn heat+filter on, got %v", ft.writes[0])
	}

	// Still inside: no duplicate write.
	s.tick(context.Background(), at(11, 30))
	if len(ft.writes) != 1 {
		t.Fatalf("expected no duplicate write inside window, got %d", len(ft.writes))
	}

	// Leaving the window in heat mode: heat off, filter kept on.
	s.tick(context.Background(), at(12, 30))
	if len(ft.writes) != 2 {
		t.Fatalf("expected stop-edge write, got %d", len(ft.writes))
	}
	if ft.writes[1]["heat_power"] != 0 || ft.writes[1]["filter_power"] != 1 {
		t.Errorf("heat-mode stop should be heat off, filter on, got %v", ft.writes[1])
	}
}

func TestTickStartupReconcile(t *testing.T) {
	ft := &fakeTub{}
	s := New(filepath.Join(t.TempDir(), "schedules.json"), ft, nil)
	if _, err := s.Replace([]Schedule{
		{Enabled: true, Start: "10:00", Stop: "12:00", Mode: ModeAll},
	}); err != nil {
		t.Fatal(err)
	}
	// First-ever tick already inside the window should reconcile to ON.
	s.tick(context.Background(), at(11, 0))
	if len(ft.writes) != 1 || ft.writes[0]["heat_power"] != 1 {
		t.Fatalf("startup inside window should turn on, got %v", ft.writes)
	}
}

func TestTickRetriesAfterFailure(t *testing.T) {
	ft := &fakeTub{failNext: true}
	s := New(filepath.Join(t.TempDir(), "schedules.json"), ft, nil)
	if _, err := s.Replace([]Schedule{
		{Enabled: true, Start: "10:00", Stop: "12:00", Mode: ModeHeat},
	}); err != nil {
		t.Fatal(err)
	}

	// First eval inside the window, but the write fails: nothing recorded.
	s.tick(context.Background(), at(11, 0))
	if len(ft.writes) != 0 {
		t.Fatalf("expected the failed write to record nothing, got %v", ft.writes)
	}

	// Next tick still inside the window must retry — and now succeed.
	s.tick(context.Background(), at(11, 1))
	if len(ft.writes) != 1 || ft.writes[0]["heat_power"] != 1 {
		t.Fatalf("expected a retried start write, got %v", ft.writes)
	}
}

// A Replace that can't be persisted must not take effect: otherwise the client
// gets an error while the (unsaved) schedules silently run until restart.
func TestReplaceKeepsOldListWhenPersistFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	s := New(path, &fakeTub{}, nil)

	if _, err := s.Replace([]Schedule{{Enabled: true, Start: "10:00", Stop: "12:00", Mode: ModeHeat}}); err != nil {
		t.Fatal(err)
	}

	// Make the save fail: replace the data dir path with a regular file so
	// writing path+".tmp" inside it is impossible.
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocker, "schedules.json")

	_, err := s.Replace([]Schedule{{Enabled: true, Start: "06:00", Stop: "08:00", Mode: ModeAll}})
	if err == nil {
		t.Fatal("expected an error when persisting fails")
	}
	got := s.List()
	if len(got) != 1 || got[0].Start != "10:00" {
		t.Errorf("failed Replace must leave the old list active, got %v", got)
	}
}

func TestReplacePersistsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	s := New(path, &fakeTub{}, nil)

	if _, err := s.Replace([]Schedule{{Enabled: true, Start: "bad", Stop: "12:00", Mode: ModeHeat}}); err == nil {
		t.Error("expected error for bad start time")
	}
	if _, err := s.Replace([]Schedule{{Enabled: true, Start: "10:00", Stop: "12:00", Mode: "nope"}}); err == nil {
		t.Error("expected error for bad mode")
	}

	saved, err := s.Replace([]Schedule{{Enabled: true, Start: "10:00", Stop: "12:00", Mode: ModeHeat}})
	if err != nil {
		t.Fatal(err)
	}
	if saved[0].ID == "" {
		t.Error("expected an ID to be assigned")
	}

	// A fresh scheduler should load what we persisted.
	s2 := New(path, &fakeTub{}, nil)
	if got := s2.List(); len(got) != 1 || got[0].Start != "10:00" {
		t.Errorf("reload mismatch: %v", got)
	}
}
