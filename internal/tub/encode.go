package tub

import (
	"encoding/binary"
	"fmt"
)

// WritableAttr describes one Gizwits writable datapoint for our Airjet.
type WritableAttr struct {
	ID   int    // canonical Gizwits id 0..13
	Name string // wire name in our JSON API
	Kind string // "bool", "enum", "uint8", "uint16"
	Bit  int    // bit position in wBitBuf; only used for bool/enum (-1 otherwise)
}

// Writable attribute table for Airjet (P182D102), in id order.
// Mirrors the WRITABLE_ATTRS list in the Node version.
var Writable = []WritableAttr{
	{0, "power", "bool", 0},
	{1, "heat_power", "bool", 1},
	{2, "filter_power", "bool", 2},
	{3, "wave_power", "bool", 3},
	{4, "locked", "bool", 4},
	{5, "earth", "bool", 5},
	{6, "temp_set_unit", "enum", 6},
	{7, "temp_set", "uint8", -1},
	{8, "heat_appm_min", "uint16", -1},
	{9, "heat_timer_min", "uint16", -1},
	{10, "filter_appm_min", "uint16", -1},
	{11, "filter_timer_min", "uint16", -1},
	{12, "wave_appm_min", "uint16", -1},
	{13, "wave_timer_min", "uint16", -1},
}

var writableByName = func() map[string]WritableAttr {
	m := make(map[string]WritableAttr, len(Writable))
	for _, a := range Writable {
		m[a.Name] = a
	}
	return m
}()

// EncodeControl builds the p0 payload for a cmd 0x0093 (Control Device) request.
//
// Wire format (17 bytes total):
//   action(0x01) + attrFlags(2B BE bitmask of ids) + attrVals(14B: wBitBuf(1) + temp_set(1) + 6×uint16BE(12))
//
// CRITICAL: attrFlags is sent BIG-ENDIAN. The Gizwits SoC SDK calls
// gizByteOrderExchange on receipt when sizeof(attrFlags) > 1, which reverses
// the wire bytes before the C bitfield struct is read. Sending little-endian
// silently fails — the device responds with seq but no state change occurs.
//
// `current` provides values for un-flagged attrs (defensive: some firmware
// applies all bytes regardless of flags). Pass the most recent ParseStatus output.
func EncodeControl(updates map[string]any, current *Status) ([]byte, error) {
	// attrFlags
	var flagBits uint16
	for name := range updates {
		a, ok := writableByName[name]
		if !ok {
			return nil, fmt.Errorf("unknown writable attribute: %q", name)
		}
		flagBits |= 1 << a.ID
	}

	// effective values for every writable attr, merging current state + updates
	val := func(a WritableAttr) any {
		if v, ok := updates[a.Name]; ok {
			return v
		}
		if current == nil {
			return 0
		}
		switch a.Name {
		case "power":            return current.Power
		case "heat_power":       return current.HeatPower
		case "filter_power":     return current.FilterPower
		case "wave_power":       return current.WavePower
		case "locked":           return current.Locked
		case "earth":            return current.Earth
		case "temp_set_unit":    return current.TempSetUnit
		case "temp_set":         return current.TempSet
		case "heat_appm_min":    return current.HeatAppmMin
		case "heat_timer_min":   return current.HeatTimerMin
		case "filter_appm_min":  return current.FilterAppmMin
		case "filter_timer_min": return current.FilterTimerMin
		case "wave_appm_min":    return current.WaveAppmMin
		case "wave_timer_min":   return current.WaveTimerMin
		}
		return 0
	}

	// wBitBuf packs the 7 bool/enum writable attrs, one bit each at position a.Bit
	var wBit byte
	for _, a := range Writable {
		if a.Kind != "bool" && a.Kind != "enum" {
			continue
		}
		if asBool(val(a)) {
			wBit |= 1 << a.Bit
		}
	}

	out := make([]byte, 17)
	out[0] = 0x01
	binary.BigEndian.PutUint16(out[1:3], flagBits)
	out[3] = wBit
	out[4] = asUint8(val(WritableAttr{Name: "temp_set"}), val(Writable[7]))
	binary.BigEndian.PutUint16(out[5:7],  asUint16(val(Writable[8])))
	binary.BigEndian.PutUint16(out[7:9],  asUint16(val(Writable[9])))
	binary.BigEndian.PutUint16(out[9:11], asUint16(val(Writable[10])))
	binary.BigEndian.PutUint16(out[11:13],asUint16(val(Writable[11])))
	binary.BigEndian.PutUint16(out[13:15],asUint16(val(Writable[12])))
	binary.BigEndian.PutUint16(out[15:17],asUint16(val(Writable[13])))
	return out, nil
}

// asBool coerces any JSON-decoded value to bool. JSON numbers decode as float64.
func asBool(v any) bool {
	switch t := v.(type) {
	case bool:    return t
	case float64: return t != 0
	case int:     return t != 0
	case uint8:   return t != 0
	case uint16:  return t != 0
	case string:  return t == "1" || t == "true" || t == "on"
	}
	return false
}

// asUint8 coerces to uint8. Accepts multiple sources because Go's any-typed
// map values that came via JSON decoding land as float64.
func asUint8(v ...any) uint8 {
	for _, x := range v {
		switch t := x.(type) {
		case float64: return uint8(uint64(t) & 0xff)
		case int:     return uint8(uint64(t) & 0xff)
		case uint8:   return t
		case uint16:  return uint8(t & 0xff)
		case bool:    if t { return 1 }; return 0
		}
	}
	return 0
}

func asUint16(v any) uint16 {
	switch t := v.(type) {
	case float64: return uint16(uint64(t) & 0xffff)
	case int:     return uint16(uint64(t) & 0xffff)
	case uint8:   return uint16(t)
	case uint16:  return t
	case bool:    if t { return 1 }; return 0
	}
	return 0
}
