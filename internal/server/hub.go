// Package server runs JumpJump rooms over WebSocket with the game package as
// the single authority on landings and scores.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

type Config struct {
	MaxPlayers           int
	MinPlayers           int // an all-ready lobby starts once it has this many players
	Countdown            time.Duration
	IdleTimeout          time.Duration // no jump for this long forfeits the match
	IdleWarning          time.Duration // warn this long before the idle timeout
	ReadyTimeout         time.Duration // a lobby member who has not readied by then is removed
	ReadyWarning         time.Duration // warn this long before the ready timeout
	Settle               time.Duration // client landing recovery before the next charge
	JitterTolerance      time.Duration // network slack in the jump timing check
	PingInterval         time.Duration
	TickInterval         time.Duration
	MaxMessageBytes      int64
	MaxMessagesPerSecond int
}

func DefaultConfig() Config {
	return Config{
		MaxPlayers:           4,
		MinPlayers:           2,
		Countdown:            3 * time.Second,
		IdleTimeout:          time.Minute,
		IdleWarning:          15 * time.Second,
		ReadyTimeout:         15 * time.Second,
		ReadyWarning:         5 * time.Second,
		Settle:               200 * time.Millisecond,
		JitterTolerance:      300 * time.Millisecond,
		PingInterval:         10 * time.Second,
		TickInterval:         250 * time.Millisecond,
		MaxMessageBytes:      4096,
		MaxMessagesPerSecond: 20,
	}
}

// Hub owns every room. Game state is only touched by the Run goroutine;
// connection goroutines reach it through events, so nothing needs a lock.
type Hub struct {
	cfg     Config
	log     *slog.Logger
	events  chan event
	stopped chan struct{}
	rooms   map[string]*room
}

type event struct {
	client *client
	msg    clientMessage
	closed bool // the connection is gone; msg is empty
}

type client struct {
	conn *websocket.Conn
	send chan []byte
	// Owned by the hub goroutine.
	member *member
	closed bool
}

func NewHub(cfg Config, log *slog.Logger) *Hub {
	if cfg.IdleWarning >= cfg.IdleTimeout {
		cfg.IdleWarning = cfg.IdleTimeout / 4
	}
	if cfg.ReadyWarning >= cfg.ReadyTimeout {
		cfg.ReadyWarning = cfg.ReadyTimeout / 3
	}
	return &Hub{cfg: cfg, log: log, events: make(chan event), stopped: make(chan struct{}), rooms: map[string]*room{}}
}

func (h *Hub) Run(ctx context.Context) {
	defer close(h.stopped)
	ticker := time.NewTicker(h.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-h.events:
			h.handle(ev, time.Now())
		case now := <-ticker.C:
			for _, r := range h.rooms {
				for _, c := range r.tick(now) {
					h.kick(c, "ready_timeout", "did not ready in time", now)
				}
			}
		}
	}
}

func (h *Hub) post(ev event) {
	select {
	case h.events <- ev:
	case <-h.stopped:
	}
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(h.cfg.MaxMessageBytes)
	c := &client{conn: conn, send: make(chan []byte, 64)}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go c.writeLoop(ctx, h.cfg.PingInterval)
	h.readLoop(ctx, c)
}

func (h *Hub) readLoop(ctx context.Context, c *client) {
	defer h.post(event{client: c, closed: true})
	window, count := time.Now(), 0
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, count = now, 0
		}
		if count++; count > h.cfg.MaxMessagesPerSecond {
			c.conn.Close(websocket.StatusPolicyViolation, "rate limit")
			return
		}
		var msg clientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.conn.Close(websocket.StatusPolicyViolation, "malformed message")
			return
		}
		h.post(event{client: c, msg: msg})
	}
}

// writeLoop drains send until the hub closes it, then closes the connection;
// failed pings close it too, which the reader reports as a disconnect.
func (c *client) writeLoop(ctx context.Context, pingEvery time.Duration) {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-c.send:
			if !ok {
				c.conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				c.conn.CloseNow()
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				c.conn.CloseNow()
				return
			}
		}
	}
}

func (c *client) enqueue(v any) {
	if c.closed {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // only our own message types are sent
	}
	select {
	case c.send <- data:
	default:
		// A client this far behind is gone in practice; the reader reports it.
		c.conn.CloseNow()
	}
}

func (c *client) reject(code, message string) {
	c.enqueue(errorMessage{Type: "error", Code: code, Message: message})
}

func (h *Hub) handle(ev event, now time.Time) {
	c := ev.client
	if c.closed {
		return
	}
	if ev.closed {
		h.drop(c, reasonDisconnected, now)
		return
	}
	m := c.member
	if m == nil {
		if ev.msg.Type != typeJoin {
			h.kick(c, "not_joined", "first message must be join", now)
			return
		}
		h.join(c, ev.msg, now)
		return
	}
	switch ev.msg.Type {
	case typeReady:
		m.room.ready(m, now)
	case typeCharge:
		m.room.charge(m, ev.msg, now)
	case typeJump:
		m.room.jump(m, ev.msg, now)
	case typeLeave:
		h.drop(c, reasonLeft, now)
	case typeJoin:
		c.reject("already_joined", "this connection is already in a room")
	default:
		h.kick(c, "unknown_type", "unknown message type", now)
	}
}

func (h *Hub) join(c *client, msg clientMessage, now time.Time) {
	name, code := strings.TrimSpace(msg.Name), strings.TrimSpace(msg.RoomCode)
	if n := utf8.RuneCountInString(name); n == 0 || n > 16 {
		h.kick(c, "bad_name", "name must be 1-16 characters", now)
		return
	}
	if n := utf8.RuneCountInString(code); n == 0 || n > 16 {
		h.kick(c, "bad_room_code", "room code must be 1-16 characters", now)
		return
	}
	r := h.rooms[code]
	if r == nil {
		r = &room{code: code, cfg: h.cfg, log: h.log.With("room", code)}
		h.rooms[code] = r
	}
	switch {
	case r.racing():
		h.kick(c, "room_busy", "a match is in progress", now)
	case len(r.members) >= h.cfg.MaxPlayers:
		h.kick(c, "room_full", "room is full", now)
	default:
		r.add(c, name, now)
	}
}

func (h *Hub) kick(c *client, code, message string, now time.Time) {
	c.reject(code, message)
	h.drop(c, reasonDisconnected, now)
}

// drop closes the client's queue (the writer flushes it, then closes the
// connection) and removes its member, forfeiting any match in progress.
func (h *Hub) drop(c *client, reason string, now time.Time) {
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
	m := c.member
	if m == nil {
		return
	}
	m.room.remove(m, reason, now)
	if len(m.room.members) == 0 {
		delete(h.rooms, m.room.code)
	}
}
