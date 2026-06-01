package tub

import "testing"

func TestValidateUpdates(t *testing.T) {
	ok := []map[string]any{
		{"power": float64(1)},
		{"power": true},
		{"heat_power": float64(0)},
		{"temp_set": float64(38)},
		{"temp_set": float64(0)},
		{"temp_set": float64(255)},
		{"heat_timer_min": float64(120)},
		{"filter_timer_min": float64(65535)},
		{"power": 1, "temp_set": 30}, // native ints from non-JSON callers
	}
	for _, u := range ok {
		if err := ValidateUpdates(u); err != nil {
			t.Errorf("ValidateUpdates(%v) = %v, want nil", u, err)
		}
	}

	bad := []map[string]any{
		{"nope": float64(1)},            // unknown attribute
		{"power": float64(2)},           // bool out of 0/1
		{"power": "on"},                 // bool wrong type
		{"temp_set": float64(300)},      // uint8 overflow (would wrap to 44)
		{"temp_set": float64(38.5)},     // non-integer
		{"temp_set": "hot"},             // non-numeric
		{"heat_timer_min": float64(-1)}, // below range
		{"heat_timer_min": float64(70000)},
	}
	for _, u := range bad {
		if err := ValidateUpdates(u); err == nil {
			t.Errorf("ValidateUpdates(%v) = nil, want error", u)
		}
	}
}
