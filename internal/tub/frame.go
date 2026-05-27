// Package tub speaks the Gizwits GAgent LAN protocol to a Bestway Airjet hot tub.
// Reference: github.com/Apollon77/node-ph803w/blob/main/PROTOCOL.md
package tub

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// preamble that every frame starts with.
var preamble = []byte{0x00, 0x00, 0x00, 0x03}

// Command codes as documented in the Gizwits LAN protocol.
const (
	CmdDiscoverReq  = 0x0003 // UDP App→Dev
	CmdDiscoverResp = 0x0004 // UDP Dev→App
	CmdPasscodeReq  = 0x0006 // TCP App→Dev
	CmdPasscodeResp = 0x0007 // TCP Dev→App
	CmdLoginReq     = 0x0008 // TCP App→Dev
	CmdLoginResp    = 0x0009 // TCP Dev→App
	CmdPing         = 0x0015 // TCP App→Dev (heartbeat, every 4s)
	CmdPong         = 0x0016 // TCP Dev→App
	CmdReadReq      = 0x0090 // TCP App→Dev (payload 0x02 = read all)
	CmdReadResp     = 0x0091 // TCP Dev→App (p0[0]=0x03 status, 0x04 push)
	CmdWriteReq     = 0x0093 // TCP App→Dev (seq[4] + p0)
	CmdWriteResp    = 0x0094 // TCP Dev→App
)

// Frame is one Gizwits protocol message: command + payload (no header).
type Frame struct {
	Cmd     uint16
	Payload []byte
}

// EncodeFrame builds the wire bytes for a frame.
// Layout: preamble(4) + varint length + flag(1) + cmd(2 BE) + payload.
func EncodeFrame(cmd uint16, payload []byte) []byte {
	body := make([]byte, 3+len(payload))
	body[0] = 0x00 // flag
	binary.BigEndian.PutUint16(body[1:3], cmd)
	copy(body[3:], payload)

	var lenBuf []byte
	if len(body) < 0x80 {
		lenBuf = []byte{byte(len(body))}
	} else {
		lenBuf = []byte{0x80 | byte(len(body)>>8), byte(len(body) & 0xff)}
	}
	out := make([]byte, 0, len(preamble)+len(lenBuf)+len(body))
	out = append(out, preamble...)
	out = append(out, lenBuf...)
	out = append(out, body...)
	return out
}

// ParseFrames consumes as many complete frames as possible from buf and returns
// them along with any unconsumed trailing bytes.
func ParseFrames(buf []byte) ([]Frame, []byte, error) {
	var out []Frame
	off := 0
	for {
		if off+6 > len(buf) {
			break
		}
		if !bytes.Equal(buf[off:off+4], preamble) {
			// resync by dropping a byte; the wire shouldn't desync but be defensive
			off++
			continue
		}
		lenByte := buf[off+4]
		var headerLen, bodyLen int
		if lenByte&0x80 != 0 {
			if off+6 > len(buf) {
				break
			}
			bodyLen = int(lenByte&0x7f)<<8 | int(buf[off+5])
			headerLen = 2
		} else {
			bodyLen = int(lenByte)
			headerLen = 1
		}
		total := 4 + headerLen + bodyLen
		if off+total > len(buf) {
			break
		}
		body := buf[off+4+headerLen : off+total]
		if len(body) < 3 {
			return nil, nil, fmt.Errorf("frame body too short: %d", len(body))
		}
		cmd := binary.BigEndian.Uint16(body[1:3])
		f := Frame{Cmd: cmd, Payload: append([]byte(nil), body[3:]...)}
		out = append(out, f)
		off += total
	}
	return out, buf[off:], nil
}
