// Package server runs JumpJump rooms over WebSocket with the game package as
// the single authority on landings and scores.
package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
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
	cfg        Config
	log        *slog.Logger
	events     chan event
	stopped    chan struct{}
	rooms      map[int]*room
	nextRoomID int
	// Connections outside any room, which are shown the room list; true once
	// a client has the latest list.
	browsers  map[*client]bool
	roomsSent []byte // the latest list sent to browsers
}

type event struct {
	client *client
	msg    clientMessage
	opened bool // a new connection; msg is empty
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
	return &Hub{cfg: cfg, log: log, events: make(chan event), stopped: make(chan struct{}),
		rooms: map[int]*room{}, browsers: map[*client]bool{}}
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
					c.reject("ready_timeout", "did not ready in time")
					h.leave(c, "ready_timeout", now)
				}
			}
		}
		h.publishRooms()
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
	h.post(event{client: c, opened: true})
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
	c.enqueueData(marshal(v))
}

func (c *client) enqueueData(data []byte) {
	if c.closed {
		return
	}
	select {
	case c.send <- data:
	default:
		// A client this far behind is gone in practice; the reader reports it.
		c.conn.CloseNow()
	}
}

func marshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // only our own message types are sent
	}
	return data
}

func (c *client) reject(code, message string) {
	c.enqueue(errorMessage{Type: "error", Code: code, Message: message})
}

func (h *Hub) handle(ev event, now time.Time) {
	c := ev.client
	if c.closed {
		return
	}
	switch {
	case ev.opened:
		h.browsers[c] = false
		return
	case ev.closed:
		h.drop(c, reasonDisconnected, now)
		return
	}
	m := c.member
	if m == nil {
		switch ev.msg.Type {
		case typeCreate:
			h.create(c, ev.msg, now)
		case typeJoin:
			h.join(c, ev.msg, now)
		case typeLeave: // already out of any room
		case typeReady, typeCharge, typeJump:
			c.reject("not_joined", "not in a room")
		default:
			h.kick(c, "unknown_type", "unknown message type", now)
		}
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
		h.leave(c, reasonLeft, now)
	case typeCreate, typeJoin:
		c.reject("already_joined", "this connection is already in a room")
	default:
		h.kick(c, "unknown_type", "unknown message type", now)
	}
}

// create opens a room with the client as its host. An empty password leaves
// it open to everyone.
func (h *Hub) create(c *client, msg clientMessage, now time.Time) {
	name, password := strings.TrimSpace(msg.Name), strings.TrimSpace(msg.Password)
	if !validName(name) {
		c.reject("bad_name", "name must be 1-16 characters")
		return
	}
	if utf8.RuneCountInString(password) > 16 {
		c.reject("bad_password", "password must be at most 16 characters")
		return
	}
	h.nextRoomID++
	r := &room{id: h.nextRoomID, password: password, cfg: h.cfg, log: h.log.With("room", h.nextRoomID)}
	h.rooms[r.id] = r
	r.log.Info("create", "locked", password != "")
	h.enter(c, r, name, now)
}

// join enters a room from the list. Refusals leave the client on the list,
// free to pick another room or retype the password.
func (h *Hub) join(c *client, msg clientMessage, now time.Time) {
	name := strings.TrimSpace(msg.Name)
	r := h.rooms[msg.RoomID]
	switch {
	case !validName(name):
		c.reject("bad_name", "name must be 1-16 characters")
	case r == nil:
		c.reject("no_room", "the room no longer exists")
	case r.racing():
		c.reject("room_busy", "a match is in progress")
	case len(r.members) >= h.cfg.MaxPlayers:
		c.reject("room_full", "room is full")
	case strings.TrimSpace(msg.Password) != r.password:
		c.reject("wrong_password", "wrong room password")
	default:
		h.enter(c, r, name, now)
	}
}

func validName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n > 0 && n <= 16
}

func (h *Hub) enter(c *client, r *room, name string, now time.Time) {
	delete(h.browsers, c)
	r.add(c, name, now)
}

// leave takes the client out of its room, forfeiting any match in progress,
// and back to the room list.
func (h *Hub) leave(c *client, reason string, now time.Time) {
	m := c.member
	c.member = nil
	h.removeMember(m, reason, now)
	h.browsers[c] = false
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
	delete(h.browsers, c)
	if m := c.member; m != nil {
		h.removeMember(m, reason, now)
	}
}

func (h *Hub) removeMember(m *member, reason string, now time.Time) {
	m.room.remove(m, reason, now)
	if len(m.room.members) == 0 {
		delete(h.rooms, m.room.id)
		m.room.log.Info("close")
	}
}

// publishRooms keeps browsers' room lists current: everyone gets the list
// when it changes, newcomers get it regardless.
func (h *Hub) publishRooms() {
	if len(h.browsers) == 0 {
		return
	}
	rooms := make([]roomInfo, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r.info())
	}
	slices.SortFunc(rooms, func(a, b roomInfo) int { return cmp.Compare(a.RoomID, b.RoomID) })
	data := marshal(roomsMessage{Type: "rooms", Rooms: rooms})
	changed := !bytes.Equal(data, h.roomsSent)
	h.roomsSent = data
	for c, current := range h.browsers {
		if changed || !current {
			c.enqueueData(data)
			h.browsers[c] = true
		}
	}
}
