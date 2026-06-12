package tub

import "testing"

// Map must include the read-only fields so `tubctl watch` can diff them —
// without temp_now in the map, watch never reports water-temperature changes.
func TestMapIncludesReadOnlyFields(t *testing.T) {
	s := &Status{TempNow: 37, TempSet: 38, HeatTempReach: true, Errors: []string{"E02_no_water_flow"}}
	m := s.Map()

	if got, ok := m["temp_now"]; !ok || got != 37 {
		t.Errorf("temp_now = %v (present=%v), want 37", got, ok)
	}
	if got, ok := m["heat_temp_reach"]; !ok || got != true {
		t.Errorf("heat_temp_reach = %v (present=%v), want true", got, ok)
	}
	if got, ok := m["errors"]; !ok {
		t.Errorf("errors missing from map, want %v", s.Errors)
	} else if e, isSlice := got.([]string); !isSlice || len(e) != 1 || e[0] != "E02_no_water_flow" {
		t.Errorf("errors = %v, want [E02_no_water_flow]", got)
	}
}
