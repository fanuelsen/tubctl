package tub

import (
	"encoding/binary"
	"fmt"
)

// Status is one decoded snapshot of the tub's state.
// JSON tags match the existing API surface so the existing frontend works unchanged.
type Status struct {
	Power          bool     `json:"power"`
	HeatPower      bool     `json:"heat_power"`
	FilterPower    bool     `json:"filter_power"`
	WavePower      bool     `json:"wave_power"`
	Locked         bool     `json:"locked"`
	Earth          bool     `json:"earth"`
	TempSetUnit    int      `json:"temp_set_unit"` // 0=F, 1=C (raw)
	TempUnit       string   `json:"temp_unit"`     // "C" or "F" (friendly)
	TempSet        uint8    `json:"temp_set"`
	HeatAppmMin    uint16   `json:"heat_appm_min"`
	HeatTimerMin   uint16   `json:"heat_timer_min"`
	FilterAppmMin  uint16   `json:"filter_appm_min"`
	FilterTimerMin uint16   `json:"filter_timer_min"`
	WaveAppmMin    uint16   `json:"wave_appm_min"`
	WaveTimerMin   uint16   `json:"wave_timer_min"`
	TempNow        uint8    `json:"temp_now"`
	HeatTempReach  bool     `json:"heat_temp_reach"`
	Errors         []string `json:"errors"`
}

var errorBits = [...]string{
	"E01_flow_switch_stuck",
	"E02_no_water_flow",
	"E03_water_under_4c",
	"E04_water_over_50c",
	"E05_temp_sensor_fault",
	"END_sleep_mode",
	"GCF_ground_fault",
	"E08_temp_switch_failure",
}

// ParseStatus decodes a cmd 0x0091 p0 payload into a Status.
// p0[0] is 0x03 (response to read) or 0x04 (auto-pushed state-change report).
// Both carry the same 17-byte payload layout starting at p0[1].
func ParseStatus(p0 []byte) (*Status, error) {
	if len(p0) < 18 {
		return nil, fmt.Errorf("status payload too short: %d bytes", len(p0))
	}
	if p0[0] != 0x03 && p0[0] != 0x04 {
		return nil, fmt.Errorf("expected status magic 0x03/0x04, got 0x%02x", p0[0])
	}
	d := p0[1:]
	b0 := d[0]

	s := &Status{
		Power:          b0&0x01 != 0,
		HeatPower:      b0&0x02 != 0,
		FilterPower:    b0&0x04 != 0,
		WavePower:      b0&0x08 != 0,
		Locked:         b0&0x10 != 0,
		Earth:          b0&0x20 != 0,
		TempSet:        d[1],
		HeatAppmMin:    binary.BigEndian.Uint16(d[2:4]),
		HeatTimerMin:   binary.BigEndian.Uint16(d[4:6]),
		FilterAppmMin:  binary.BigEndian.Uint16(d[6:8]),
		FilterTimerMin: binary.BigEndian.Uint16(d[8:10]),
		WaveAppmMin:    binary.BigEndian.Uint16(d[10:12]),
		WaveTimerMin:   binary.BigEndian.Uint16(d[12:14]),
		TempNow:        d[14],
		HeatTempReach:  d[15]&0x01 != 0,
		Errors:         decodeErrors(d[16]),
	}
	if b0&0x40 != 0 {
		s.TempSetUnit, s.TempUnit = 1, "C"
	} else {
		s.TempSetUnit, s.TempUnit = 0, "F"
	}
	return s, nil
}

// Map renders the status as the attribute map the HTTP API and CLI compare
// against. Single source of truth so the two call sites can't drift apart.
func (s *Status) Map() map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"power":            s.Power,
		"heat_power":       s.HeatPower,
		"filter_power":     s.FilterPower,
		"wave_power":       s.WavePower,
		"locked":           s.Locked,
		"earth":            s.Earth,
		"temp_set_unit":    s.TempSetUnit,
		"temp_set":         int(s.TempSet),
		"heat_appm_min":    int(s.HeatAppmMin),
		"heat_timer_min":   int(s.HeatTimerMin),
		"filter_appm_min":  int(s.FilterAppmMin),
		"filter_timer_min": int(s.FilterTimerMin),
		"wave_appm_min":    int(s.WaveAppmMin),
		"wave_timer_min":   int(s.WaveTimerMin),
		// Read-only fields. Write paths only ever look up the attrs a request
		// asked for (all writable), so these extra keys are inert there — but
		// `tubctl watch` diffs the whole map and needs them to report changes.
		"temp_now":        int(s.TempNow),
		"heat_temp_reach": s.HeatTempReach,
		"errors":          s.Errors,
	}
}

func decodeErrors(b byte) []string {
	out := []string{}
	for i, name := range errorBits {
		if b&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}
