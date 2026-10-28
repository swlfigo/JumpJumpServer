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

func (c *testClient) join(name, room string) int {
	c.t.Helper()
	c.send(map[string]any{"type": "join", "name": name, "roomCode": room})
	return expect[welcomeMessage](c, "welcome").PlayerID
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
	a, b := dial(t, url), dial(t, url)
	aID, bID := a.join("A", "room"), b.join("B", "room")
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
	stranger.expectClosed()

	a := dial(t, url)
	a.join("A", "solo")
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
	b.join("B", "other")
	b.send(map[string]any{"type": "teleport", "x": 100})
	b.expectError("unknown_type")
	b.expectClosed()

	c := dial(t, url)
	c.send(map[string]any{"type": "join", "name": strings.Repeat("x", 5000), "roomCode": "big"})
	c.expectClosed()
}

func TestRejectsEarlyJumps(t *testing.T) {
	cfg := testConfig()
	cfg.MinPlayers, cfg.JitterTolerance = 1, 0
	url := startServer(t, cfg)
	a := dial(t, url)
	a.join("A", "solo")
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
	a, b := dial(t, url), dial(t, url)
	aID, bID := a.join("A", "room"), b.join("B", "room")
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
	a, b := dial(t, url), dial(t, url)
	aID, bID := a.join("A", "room"), b.join("B", "room")
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

func TestRoomFullAndBusy(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPlayers = 2
	url := startServer(t, cfg)
	a, b := dial(t, url), dial(t, url)
	a.join("A", "room")
	b.join("B", "room")
	c := dial(t, url)
	c.send(map[string]any{"type": "join", "name": "C", "roomCode": "room"})
	c.expectError("room_full")
	startMatch(t, a, b)
	d := dial(t, url)
	d.send(map[string]any{"type": "join", "name": "D", "roomCode": "room"})
	d.expectError("room_busy")
}
