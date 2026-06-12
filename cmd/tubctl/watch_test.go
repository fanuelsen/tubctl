package main

import (
	"strings"
	"testing"

	"sublog.org/tubctl/internal/tub"
)

// fmtState must show the current water temperature (temp_now), not temp_set twice.
func TestFmtStateShowsCurrentAndTargetTemp(t *testing.T) {
	s := &tub.Status{TempNow: 31, TempSet: 38}
	out := fmtState(s.Map())

	if !strings.Contains(out, "now=31") {
		t.Errorf("fmtState should show current temp: now=31, got %q", out)
	}
	if !strings.Contains(out, "set=38") {
		t.Errorf("fmtState should show target temp: set=38, got %q", out)
	}
}
