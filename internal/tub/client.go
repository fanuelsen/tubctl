package tub

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const heartbeatInterval = 4 * time.Second

// Client speaks the Gizwits LAN protocol to one tub.
// Safe for concurrent use; serializes reads/writes internally.
type Client struct {
	addr string
	log  *slog.Logger

	mu        sync.Mutex
	conn      net.Conn
	loggedIn  bool
	writeSeq  uint32
	stopHB    chan struct{}
	readCh    chan *Status
	writeCh   map[uint32]chan struct{} // seq → done signal
	connectMu sync.Mutex               // serializes Connect attempts

	subMu  sync.Mutex
	subs   map[int]chan *Status
	subSeq int
}

// NewClient prepares a client. Call Connect before Read/Write.
func NewClient(host string, port int, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		addr:    net.JoinHostPort(host, fmt.Sprint(port)),
		log:     log,
		readCh:  make(chan *Status, 8),
		writeCh: make(map[uint32]chan struct{}),
		subs:    make(map[int]chan *Status),
	}
}

// Connect opens the TCP connection, requests the passcode, logs in, and
// starts the read loop and heartbeat. Subsequent calls while already connected
// are no-ops. After a disconnect, Connect can be called again to reconnect.
func (c *Client) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.Lock()
	if c.loggedIn {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.addr, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.writeSeq = 0
	c.stopHB = make(chan struct{})
	// drain any stale items from readCh
	for {
		select {
		case <-c.readCh:
		default:
			goto drained
		}
	}
drained:
	c.mu.Unlock()

	// reader goroutine
	loginDone := make(chan error, 1)
	go c.readLoop(loginDone)

	// kick off the handshake
	if _, err := conn.Write(EncodeFrame(CmdPasscodeReq, nil)); err != nil {
		conn.Close()
		return fmt.Errorf("send passcode req: %w", err)
	}

	select {
	case err := <-loginDone:
		if err != nil {
			conn.Close()
			return err
		}
	case <-ctx.Done():
		conn.Close()
		return ctx.Err()
	case <-time.After(8 * time.Second):
		conn.Close()
		return errors.New("login timeout")
	}

	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	go c.heartbeatLoop()
	c.log.Info("tub connected", "addr", c.addr)
	return nil
}

// Close tears down the connection. Safe to call concurrently.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopHB != nil {
		close(c.stopHB)
		c.stopHB = nil
	}
	c.loggedIn = false
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// LoggedIn reports whether the client has an active authenticated session.
func (c *Client) LoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

// Read requests the current status and returns the parsed Status.
func (c *Client) Read(ctx context.Context) (*Status, error) {
	if !c.LoggedIn() {
		return nil, errors.New("not logged in")
	}
	// drain any pushed-but-not-consumed status frames first to get the freshest read
	for {
		select {
		case <-c.readCh:
		default:
			goto sent
		}
	}
sent:
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("not connected")
	}
	if _, err := conn.Write(EncodeFrame(CmdReadReq, []byte{0x02})); err != nil {
		return nil, fmt.Errorf("send read req: %w", err)
	}

	select {
	case s := <-c.readCh:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(4 * time.Second):
		return nil, errors.New("read timeout")
	}
}

// Write applies one or more attribute updates. `current` is the last known
// state used to fill un-flagged attrs (defensive). It may be nil; the client
// will then send zeros for un-flagged attrs which may or may not be honored.
func (c *Client) Write(ctx context.Context, updates map[string]any, current *Status) error {
	if !c.LoggedIn() {
		return errors.New("not logged in")
	}
	p0, err := EncodeControl(updates, current)
	if err != nil {
		return err
	}
	seq := atomic.AddUint32(&c.writeSeq, 1)
	body := make([]byte, 4+len(p0))
	binary.BigEndian.PutUint32(body[:4], seq)
	copy(body[4:], p0)

	done := make(chan struct{}, 1)
	c.mu.Lock()
	c.writeCh[seq] = done
	conn := c.conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.writeCh, seq)
		c.mu.Unlock()
	}()

	if conn == nil {
		return errors.New("not connected")
	}
	if _, err := conn.Write(EncodeFrame(CmdWriteReq, body)); err != nil {
		return fmt.Errorf("send write req: %w", err)
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(4 * time.Second):
		return errors.New("write timeout (no cmd 0x94 received)")
	}
}

// readLoop pumps the TCP connection and dispatches frames.
// Signals login result on loginDone (nil = ok, non-nil = err).
func (c *Client) readLoop(loginDone chan<- error) {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	for {
		n, err := conn.Read(tmp)
		if err != nil {
			c.log.Debug("read loop ended", "err", err)
			c.markDisconnected()
			return
		}
		buf = append(buf, tmp[:n]...)
		frames, rest, perr := ParseFrames(buf)
		if perr != nil {
			c.log.Warn("frame parse error", "err", perr)
			buf = buf[:0]
			continue
		}
		buf = rest
		for _, f := range frames {
			c.dispatch(f, loginDone)
		}
	}
}

func (c *Client) dispatch(f Frame, loginDone chan<- error) {
	switch f.Cmd {
	case CmdPasscodeResp:
		// payload: 2-byte length + N bytes passcode
		if len(f.Payload) < 2 {
			select { case loginDone <- errors.New("short passcode resp"): default: }
			return
		}
		pcLen := int(binary.BigEndian.Uint16(f.Payload[:2]))
		if len(f.Payload) < 2+pcLen {
			select { case loginDone <- errors.New("truncated passcode"): default: }
			return
		}
		passcode := f.Payload[2 : 2+pcLen]
		// LoginReq: 2-byte length + passcode echo
		body := make([]byte, 2+pcLen)
		binary.BigEndian.PutUint16(body[:2], uint16(pcLen))
		copy(body[2:], passcode)
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			conn.Write(EncodeFrame(CmdLoginReq, body))
		}

	case CmdLoginResp:
		if len(f.Payload) >= 1 && f.Payload[0] == 0x00 {
			select { case loginDone <- nil: default: }
		} else {
			select { case loginDone <- errors.New("login rejected"): default: }
		}

	case CmdReadResp:
		s, err := ParseStatus(f.Payload)
		if err != nil {
			c.log.Warn("parse status failed", "err", err)
			return
		}
		select {
		case c.readCh <- s:
		default:
			// channel full; drop the oldest and push
			<-c.readCh
			c.readCh <- s
		}
		// Fan out to any active subscribers (SSE clients, etc.) so they get both
		// device-pushed 0x04 state-change reports and our own 0x03 read responses.
		c.broadcast(s)

	case CmdWriteResp:
		if len(f.Payload) < 4 {
			return
		}
		seq := binary.BigEndian.Uint32(f.Payload[:4])
		c.mu.Lock()
		ch := c.writeCh[seq]
		c.mu.Unlock()
		if ch != nil {
			select { case ch <- struct{}{}: default: }
		}

	case CmdPong:
		// heartbeat ack — ignore
	}
}

func (c *Client) heartbeatLoop() {
	c.mu.Lock()
	stop := c.stopHB
	c.mu.Unlock()
	if stop == nil {
		return
	}
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.mu.Lock()
			conn := c.conn
			loggedIn := c.loggedIn
			c.mu.Unlock()
			if !loggedIn || conn == nil {
				return
			}
			if _, err := conn.Write(EncodeFrame(CmdPing, nil)); err != nil {
				return
			}
		case <-stop:
			return
		}
	}
}

// Subscribe registers a channel that receives every parsed Status the client
// observes — both auto-pushed (0x04) and read-response (0x03) frames.
// The returned cancel func unregisters the subscriber and closes the channel;
// callers must invoke it (defer is fine) to avoid leaking goroutines on
// disconnect.
func (c *Client) Subscribe(buffer int) (<-chan *Status, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan *Status, buffer)
	c.subMu.Lock()
	c.subSeq++
	id := c.subSeq
	c.subs[id] = ch
	c.subMu.Unlock()
	return ch, func() {
		c.subMu.Lock()
		if existing, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(existing)
		}
		c.subMu.Unlock()
	}
}

// broadcast delivers s to all current subscribers without blocking. A slow
// consumer's oldest queued value is discarded in favour of the new one.
func (c *Client) broadcast(s *Status) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- s:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- s:
			default:
			}
		}
	}
}

func (c *Client) markDisconnected() {
	c.mu.Lock()
	c.loggedIn = false
	if c.stopHB != nil {
		close(c.stopHB)
		c.stopHB = nil
	}
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	// Drop subscribers so any SSE handlers waiting on the channel return
	// promptly; clients can then reconnect through ensureConnected.
	c.subMu.Lock()
	for id, ch := range c.subs {
		close(ch)
		delete(c.subs, id)
	}
	c.subMu.Unlock()
}
