package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"jumpjump-server/internal/game"
)

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.Countdown = 20 * time.Millisecond
	cfg.TickInterval = 10 * time.Millisecond
	cfg.JitterTolerance = time.Hour // most tests don't wait out flights; TestRejectsEarlyJumps tightens it
	return cfg
}

func startServer(t *testing.T, cfg Config) string {
	t.Helper()
	hub := NewHub(cfg, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	srv := httptest.NewServer(hub)
	t.Cleanup(func() { srv.Close(); cancel() })
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

type testClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, url string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &testClient{t: t, conn: conn}
}

func (c *testClient) send(v any) {
	c.t.Helper()
	data, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("send %s: %v", data, err)
	}
}

// create opens a room and returns the creator's welcome, which has the room ID.
func (c *testClient) create(name, password string) welcomeMessage {
	c.t.Helper()
	c.send(map[string]any{"type": "create", "name": name, "password": password})
	return expect[welcomeMessage](c, "welcome")
}

// join enters an open room and returns the player ID.
func (c *testClient) join(name string, roomID int) int {
	c.t.Helper()
	c.send(map[string]any{"type": "join", "name": name, "roomID": roomID})
	return expect[welcomeMessage](c, "welcome").PlayerID
}

// pair puts two new clients in a fresh open room.
func pair(t *testing.T, url string) (a, b *testClient, aID, bID int) {
	t.Helper()
	a, b = dial(t, url), dial(t, url)
	welcome := a.create("A", "")
	return a, b, welcome.PlayerID, b.join("B", welcome.RoomID)
}

func (c *testClient) jump(seq, pressMs int) {
	c.t.Helper()
	c.send(map[string]any{"type": "jump", "seq": seq, "pressMs": pressMs})
}

// expect reads until a message of the given type arrives, skipping others.
func expect[T any](c *testClient, typ string) T {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.t.Fatalf("waiting for %s: %v", typ, err)
		}
		var head struct{ Type string }
		json.Unmarshal(data, &head)
		if head.Type == typ {
			var msg T
			if err := json.Unmarshal(data, &msg); err != nil {
				c.t.Fatalf("decode %s: %v", data, err)
			}
			return msg
		}
	}
}

func (c *testClient) expectError(code string) {
	c.t.Helper()
	if got := expect[errorMessage](c, "error"); got.Code != code {
		c.t.Fatalf("error %q (%s), want %q", got.Code, got.Message, code)
	}
}

// expectClosed reads until the server closes the connection.
func (c *testClient) expectClosed() {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		if _, _, err := c.conn.Read(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				c.t.Fatal("connection still open")
			}
			return
		}
	}
}

// perfectPress is what an honest client's own prediction would find: the
// press whose landing is closest to the next platform's center.
func perfectPress(t *testing.T, course *game.Course, p game.Player) int {
	t.Helper()
	target := course.Platform(p.Index + 1).Center
	best, bestDistance := 0, math.Inf(1)
	for ms := 0; ms <= 3000; ms++ {
		landing := game.NewTrajectory(p.Position, target, game.PressSeconds(ms)).Landing()
		if d := math.Hypot(float64(landing.X-target.X), float64(landing.Z-target.Z)); d < bestDistance {
			best, bestDistance = ms, d
		}
	}
	return best
}

func startMatch(t *testing.T, clients ...*testClient) int {
	t.Helper()
	for _, c := range clients {
		c.send(map[string]any{"type": "ready"})
	}
	seed := 0
	for _, c := range clients {
		start := expect[startMessage](c, "start")
		if seed != 0 && start.Seed != seed {
			t.Fatalf("players got different seeds %d and %d", seed, start.Seed)
		}
		seed = start.Seed
	}
	time.Sleep(30 * time.Millisecond) // wait out the countdown like a real client
	return seed
}

func TestMatchFlow(t *testing.T) {
	url := startServer(t, testConfig())
	a, b, aID, bID := pair(t, url)
	seed := startMatch(t, a, b)
	course := game.NewCourse(seed)
	var predicted game.Player

	// Forged landing/result/score fields are ignored: a 0ms press lands where
	// the player stands, whatever the client claims.
	a.send(map[string]any{"type": "jump", "seq": 1, "pressMs": 0, "landingBits": []uint32{0x41200000, 0}, "result": "perfect", "score": 999})
	predicted.Jump(course, 0)
	for _, c := range []*testClient{a, b} {
		got := expect[jumpedMessage](c, "jumped")
		if got.PlayerID != aID || got.Seq != 1 || got.Result != "stayed" || got.Score != 0 || got.LandingBits != [2]uint32{} {
			t.Fatalf("forged jump: %+v", got)
		}
	}

	// Charges are relayed to everyone else.
	a.send(map[string]any{"type": "charge", "seq": 2})
	if got := expect[chargeMessage](b, "charge"); got.PlayerID != aID || got.Seq != 2 {
		t.Fatalf("charge relay: %+v", got)
	}

	// The client-side prediction matches the server bit for bit.
	press := perfectPress(t, course, predicted)
	jump := predicted.Jump(course, game.PressSeconds(press))
	a.jump(2, press)
	got := expect[jumpedMessage](b, "jumped")
	want := [2]uint32{math.Float32bits(jump.Landing.X), math.Float32bits(jump.Landing.Z)}
	if got.Result != "perfect" || got.Score != 2 || got.Combo != 1 || got.LandingBits != want {
		t.Fatalf("perfect jump: %+v, predicted landing %v", got, want)
	}

	b.jump(1, 3000)
	if got := expect[outMessage](a, "out"); got.PlayerID != bID || got.Reason != reasonFell {
		t.Fatalf("B out: %+v", got)
	}
	a.jump(3, 3000)
	result := expect[resultMessage](b, "result")
	if len(result.Ranking) != 2 || result.Ranking[0].PlayerID != aID || result.Ranking[0].Score != 2 || result.Ranking[1].PlayerID != bID {
		t.Fatalf("ranking: %+v", result.Ranking)
	}
	if lobby := expect[lobbyMessage](a, "lobby"); len(lobby.Players) != 2 || lobby.Players[0].Ready {
		t.Fatalf("back to lobby: %+v", lobby)
	}
}

func TestRejectsInvalidJumps(t *testing.T) {
	cfg := testConfig()
	cfg.MinPlayers = 1
	url := startServer(t, cfg)

	stranger := dial(t, url)
	stranger.jump(1, 0)
	stranger.expectError("not_joined")

	a := dial(t, url)
	a.create("A", "")
	startMatch(t, a)
	a.jump(2, 0)
	a.expectError("bad_seq")
	for _, press := range []any{-1, 3001, nil} {
		a.send(map[string]any{"type": "jump", "seq": 1, "pressMs": press})
		a.expectError("bad_press")
	}
	a.jump(1, 0) // none of the rejected jumps were applied
	if got := expect[jumpedMessage](a, "jumped"); got.Seq != 1 || got.Result != "stayed" {
		t.Fatalf("valid jump after rejections: %+v", got)
	}
	a.send(map[string]any{"type": "jump", "seq": 2, "pressMs": 1.5})
	a.expectClosed()

	b := dial(t, url)
	b.create("B", "")
	b.send(map[string]any{"type": "teleport", "x": 100})
	b.expectError("unknown_type")
	b.expectClosed()

	c := dial(t, url)
	c.send(map[string]any{"type": "create", "name": strings.Repeat("x", 5000)})
	c.expectClosed()
}

func TestRejectsEarlyJumps(t *testing.T) {
	cfg := testConfig()
	cfg.MinPlayers, cfg.JitterTolerance = 1, 0
	url := startServer(t, cfg)
	a := dial(t, url)
	a.create("A", "")
	startMatch(t, a)
	a.jump(1, 500) // released 500ms into a match that started 20ms ago
	a.expectError("too_early")
	time.Sleep(30 * time.Millisecond)
	a.jump(1, 0)
	expect[jumpedMessage](a, "jumped")
	a.jump(2, 0) // the first jump is still in the air
	a.expectError("too_early")
	time.Sleep(600 * time.Millisecond) // 375ms flight + 200ms settle
	a.jump(2, 0)
	if got := expect[jumpedMessage](a, "jumped"); got.Seq != 2 {
		t.Fatalf("jump after landing: %+v", got)
	}
}

func TestIdleRacerForfeitsDespiteHigherScore(t *testing.T) {
	cfg := testConfig()
	cfg.IdleTimeout, cfg.IdleWarning = 300*time.Millisecond, 200*time.Millisecond
	url := startServer(t, cfg)
	a, b, aID, bID := pair(t, url)
	course := game.NewCourse(startMatch(t, a, b))

	a.jump(1, perfectPress(t, course, game.Player{}))
	if got := expect[jumpedMessage](a, "jumped"); got.Score != 2 {
		t.Fatalf("A's opening jump: %+v", got)
	}
	for seq := 1; seq <= 6; seq++ { // B keeps hopping in place; A goes quiet
		b.jump(seq, 0)
		time.Sleep(80 * time.Millisecond)
	}
	if got := expect[idleWarningMessage](a, "idleWarning"); got.SecondsLeft != 1 {
		t.Fatalf("idle warning: %+v", got)
	}
	if got := expect[outMessage](b, "out"); got.PlayerID != aID || got.Reason != reasonIdle {
		t.Fatalf("A out: %+v", got)
	}
	b.jump(7, 3000)
	result := expect[resultMessage](a, "result")
	if result.Ranking[0].PlayerID != bID || result.Ranking[1].PlayerID != aID || result.Ranking[1].Score != 2 || result.Ranking[1].Status != reasonIdle {
		t.Fatalf("ranking: %+v", result.Ranking)
	}
}

func TestDisconnectForfeits(t *testing.T) {
	url := startServer(t, testConfig())
	a, b, aID, bID := pair(t, url)
	startMatch(t, a, b)
	b.conn.CloseNow()
	if got := expect[outMessage](a, "out"); got.PlayerID != bID || got.Reason != reasonDisconnected {
		t.Fatalf("B out: %+v", got)
	}
	a.jump(1, 3000)
	result := expect[resultMessage](a, "result")
	if result.Ranking[0].PlayerID != aID || result.Ranking[1].Status != reasonDisconnected {
		t.Fatalf("ranking: %+v", result.Ranking)
	}
}

func TestPlayersGetDistinctColors(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPlayers = paletteSize
	url := startServer(t, cfg)
	last := dial(t, url).create("A", "")
	for i := 1; i < paletteSize; i++ {
		c := dial(t, url)
		c.send(map[string]any{"type": "join", "name": string(rune('A' + i)), "roomID": last.RoomID})
		last = expect[welcomeMessage](c, "welcome")
	}
	seen := map[int]bool{}
	for _, p := range last.Players {
		if p.Color < 0 || p.Color >= paletteSize || seen[p.Color] {
			t.Fatalf("color %d out of range or repeated: %+v", p.Color, last.Players)
		}
		seen[p.Color] = true
	}
}

func TestUnreadyMemberIsWarnedThenRemoved(t *testing.T) {
	cfg := testConfig()
	cfg.ReadyTimeout, cfg.ReadyWarning = 400*time.Millisecond, 250*time.Millisecond
	url := startServer(t, cfg)
	a, b, aID, _ := pair(t, url)
	a.send(map[string]any{"type": "ready"})
	if got := expect[readyWarningMessage](b, "readyWarning"); got.SecondsLeft != 1 {
		t.Fatalf("ready warning: %+v", got)
	}
	b.expectError("ready_timeout")
	// B is back on the room list, still connected.
	if got := expect[roomsMessage](b, "rooms"); len(got.Rooms) != 1 || got.Rooms[0].Players != 1 {
		t.Fatalf("room list after removal: %+v", got)
	}
	// A readied in time and stays, now alone in the room.
	for {
		lobby := expect[lobbyMessage](a, "lobby")
		if len(lobby.Players) == 1 && lobby.Players[0].PlayerID == aID && lobby.Players[0].Ready {
			break
		}
	}
}

func TestReadyClockRestartsAfterRace(t *testing.T) {
	cfg := testConfig()
	cfg.ReadyTimeout, cfg.ReadyWarning = 400*time.Millisecond, 250*time.Millisecond
	url := startServer(t, cfg)
	a, b, _, _ := pair(t, url)
	startMatch(t, a, b)
	time.Sleep(500 * time.Millisecond) // longer than the ready timeout
	a.jump(1, 3000)
	b.jump(1, 3000)
	expect[resultMessage](a, "result")
	// Without the restart both would be removed at once, with no warning.
	for _, c := range []*testClient{a, b} {
		expect[readyWarningMessage](c, "readyWarning")
		c.expectError("ready_timeout")
	}
}

func TestRoomFullAndBusy(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPlayers = 2
	url := startServer(t, cfg)
	a, b, _, _ := pair(t, url)
	roomID := expect[roomsMessage](dial(t, url), "rooms").Rooms[0].RoomID
	c := dial(t, url)
	c.send(map[string]any{"type": "join", "name": "C", "roomID": roomID})
	c.expectError("room_full")
	startMatch(t, a, b)
	c.send(map[string]any{"type": "join", "name": "C", "roomID": roomID})
	c.expectError("room_busy")
	c.send(map[string]any{"type": "join", "name": "C", "roomID": roomID + 1})
	c.expectError("no_room")
	c.create("C", "") // refusals leave the connection on the room list
}

// nextRooms reads room lists until one satisfies ok.
func nextRooms(c *testClient, ok func([]roomInfo) bool) []roomInfo {
	c.t.Helper()
	for {
		if rooms := expect[roomsMessage](c, "rooms").Rooms; ok(rooms) {
			return rooms
		}
	}
}

func TestRoomList(t *testing.T) {
	url := startServer(t, testConfig())
	watcher := dial(t, url)
	if got := expect[roomsMessage](watcher, "rooms"); got.Rooms == nil || len(got.Rooms) != 0 {
		t.Fatalf("initial list: %+v", got)
	}

	a := dial(t, url)
	a.send(map[string]any{"type": "create", "name": "A", "password": strings.Repeat("p", 17)})
	a.expectError("bad_password")
	roomID := a.create("A", " 1234 ").RoomID
	rooms := nextRooms(watcher, func(r []roomInfo) bool { return len(r) == 1 })
	if want := (roomInfo{RoomID: roomID, Host: "A", Players: 1, MaxPlayers: 4, Locked: true}); rooms[0] != want {
		t.Fatalf("listed %+v, want %+v", rooms[0], want)
	}

	// A wrong password is refused without closing; the right one gets in.
	b := dial(t, url)
	b.send(map[string]any{"type": "join", "name": "B", "roomID": roomID})
	b.expectError("wrong_password")
	b.send(map[string]any{"type": "join", "name": "B", "roomID": roomID, "password": "4321"})
	b.expectError("wrong_password")
	b.send(map[string]any{"type": "join", "name": "B", "roomID": roomID, "password": "1234"})
	expect[welcomeMessage](b, "welcome")
	nextRooms(watcher, func(r []roomInfo) bool { return len(r) == 1 && r[0].Players == 2 })

	other := dial(t, url).create("C", "")
	nextRooms(watcher, func(r []roomInfo) bool { return len(r) == 2 && r[1].RoomID == other.RoomID && !r[1].Locked })

	startMatch(t, a, b)
	nextRooms(watcher, func(r []roomInfo) bool { return r[0].Racing })

	// Leaving goes back to the list. The room stays with B, who is now its host,
	// and closes once B leaves too.
	a.send(map[string]any{"type": "leave"})
	nextRooms(a, func(r []roomInfo) bool { return len(r) == 2 && r[0].Host == "B" && r[0].Players == 1 })
	b.send(map[string]any{"type": "leave"})
	nextRooms(watcher, func(r []roomInfo) bool { return len(r) == 1 && r[0].RoomID == other.RoomID })
	a.create("A", "")
}
