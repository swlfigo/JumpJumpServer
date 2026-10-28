package server

import (
	"cmp"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"jumpjump-server/internal/game"
)

// room is touched only by the hub goroutine.
type room struct {
	code    string
	cfg     Config
	log     *slog.Logger
	members []*member // connected, in join order
	nextID  int

	// The current match; racers is nil while the room is in the lobby.
	// Racers who disconnect stay here so they still appear in the ranking.
	racers  []*member
	course  *game.Course
	startAt time.Time
}

type member struct {
	id     int
	name   string
	client *client
	room   *room
	ready  bool

	player   game.Player
	seq      int       // last accepted jump
	readyAt  time.Time // earliest the next charge may begin: landing plus settle
	lastJump time.Time // idle timer base
	warned   bool
	out      string // reason; empty while still racing
	outAt    time.Time
}

func (r *room) racing() bool { return r.racers != nil }

func (r *room) add(c *client, name string) {
	r.nextID++
	m := &member{id: r.nextID, name: name, client: c, room: r}
	c.member = m
	r.members = append(r.members, m)
	r.log.Info("join", "player", m.id, "name", name)
	c.enqueue(welcomeMessage{Type: "welcome", PlayerID: m.id, Players: r.lobby()})
	r.broadcast(lobbyMessage{Type: "lobby", Players: r.lobby()})
}

func (r *room) remove(m *member, reason string, now time.Time) {
	r.members = slices.DeleteFunc(r.members, func(x *member) bool { return x == m })
	r.log.Info("leave", "player", m.id, "reason", reason)
	if r.racing() {
		if m.out == "" {
			r.markOut(m, reason, now)
			r.finishIfDone()
		}
		return
	}
	r.broadcast(lobbyMessage{Type: "lobby", Players: r.lobby()})
	r.maybeStart(now)
}

func (r *room) ready(m *member, now time.Time) {
	if r.racing() {
		m.client.reject("not_in_lobby", "a match is in progress")
		return
	}
	m.ready = true
	r.broadcast(lobbyMessage{Type: "lobby", Players: r.lobby()})
	r.maybeStart(now)
}

func (r *room) maybeStart(now time.Time) {
	if r.racing() || len(r.members) < r.cfg.MinPlayers {
		return
	}
	for _, m := range r.members {
		if !m.ready {
			return
		}
	}
	seed := 1 + rand.IntN(233279) // the client's generator range
	r.course = game.NewCourse(seed)
	r.startAt = now.Add(r.cfg.Countdown)
	r.racers = slices.Clone(r.members)
	for _, m := range r.racers {
		m.ready, m.player, m.seq, m.warned, m.out = false, game.Player{}, 0, false, ""
		m.readyAt, m.lastJump = r.startAt, r.startAt
	}
	r.log.Info("start", "seed", seed, "players", len(r.racers))
	r.broadcast(startMessage{Type: "start", Seed: seed, CountdownMs: int(r.cfg.Countdown / time.Millisecond)})
}

func (r *room) charge(m *member, msg clientMessage, now time.Time) {
	if !r.racing() || m.out != "" || now.Before(r.startAt) || msg.Seq != m.seq+1 {
		m.client.reject("bad_charge", "not able to charge now")
		return
	}
	for _, other := range r.members {
		if other != m {
			other.client.enqueue(chargeMessage{Type: "charge", PlayerID: m.id, Seq: msg.Seq})
		}
	}
}

func (r *room) jump(m *member, msg clientMessage, now time.Time) {
	if !r.racing() || m.out != "" {
		m.client.reject("not_racing", "not in a match")
		return
	}
	if msg.PressMs == nil || *msg.PressMs < 0 || *msg.PressMs > 3000 {
		m.client.reject("bad_press", "pressMs must be an integer from 0 to 3000")
		return
	}
	if msg.Seq != m.seq+1 {
		m.client.reject("bad_seq", "seq must be the previous jump plus one")
		return
	}
	// Releasing now means charging began pressMs ago, which cannot precede
	// the end of the previous flight plus settle (or the start signal).
	press := time.Duration(*msg.PressMs) * time.Millisecond
	if now.Add(r.cfg.JitterTolerance).Before(m.readyAt.Add(press)) {
		m.client.reject("too_early", "jump arrived before the previous one could finish")
		return
	}
	jump := m.player.Jump(r.course, game.PressSeconds(*msg.PressMs))
	m.seq, m.lastJump, m.warned = msg.Seq, now, false
	flight := time.Duration(float64(jump.Trajectory.Duration) * float64(time.Second))
	m.readyAt = now.Add(flight + r.cfg.Settle)
	r.broadcast(jumpedMessage{
		Type: "jumped", PlayerID: m.id, Seq: m.seq, PressMs: *msg.PressMs,
		LandingBits: [2]uint32{math.Float32bits(jump.Landing.X), math.Float32bits(jump.Landing.Z)},
		Result:      jump.Result.String(), Score: m.player.Score, Combo: m.player.Combo,
	})
	if jump.Result == game.Miss {
		r.markOut(m, reasonFell, now)
		r.finishIfDone()
	}
}

func (r *room) tick(now time.Time) {
	if !r.racing() {
		return
	}
	for _, m := range r.racers {
		if m.out != "" {
			continue
		}
		idle := now.Sub(m.lastJump)
		switch {
		case idle >= r.cfg.IdleTimeout:
			r.markOut(m, reasonIdle, now)
		case !m.warned && idle >= r.cfg.IdleTimeout-r.cfg.IdleWarning:
			m.warned = true
			left := r.cfg.IdleTimeout - idle
			m.client.enqueue(idleWarningMessage{Type: "idleWarning", SecondsLeft: int(math.Ceil(left.Seconds()))})
		}
	}
	r.finishIfDone()
}

func (r *room) markOut(m *member, reason string, now time.Time) {
	m.out, m.outAt = reason, now
	r.log.Info("out", "player", m.id, "reason", reason, "score", m.player.Score)
	r.broadcast(outMessage{Type: "out", PlayerID: m.id, Reason: reason})
}

func (r *room) finishIfDone() {
	if !r.racing() {
		return
	}
	for _, m := range r.racers {
		if m.out == "" {
			return
		}
	}
	ranking := rank(r.racers)
	r.log.Info("result", "ranking", ranking)
	r.broadcast(resultMessage{Type: "result", Ranking: ranking})
	r.racers, r.course = nil, nil
	r.broadcast(lobbyMessage{Type: "lobby", Players: r.lobby()})
}

// rank orders racers who fell by score (on a tie, whoever lasted longer
// first), then everyone who forfeited, the earliest forfeit last.
func rank(racers []*member) []rankEntry {
	sorted := slices.Clone(racers)
	slices.SortStableFunc(sorted, func(a, b *member) int {
		aFell, bFell := a.out == reasonFell, b.out == reasonFell
		if aFell != bFell {
			if aFell {
				return -1
			}
			return 1
		}
		if aFell && a.player.Score != b.player.Score {
			return cmp.Compare(b.player.Score, a.player.Score)
		}
		return b.outAt.Compare(a.outAt)
	})
	ranking := make([]rankEntry, len(sorted))
	for i, m := range sorted {
		ranking[i] = rankEntry{PlayerID: m.id, Name: m.name, Score: m.player.Score, Status: m.out}
	}
	return ranking
}

func (r *room) lobby() []playerInfo {
	players := make([]playerInfo, len(r.members))
	for i, m := range r.members {
		players[i] = playerInfo{PlayerID: m.id, Name: m.name, Ready: m.ready}
	}
	return players
}

func (r *room) broadcast(v any) {
	for _, m := range r.members {
		m.client.enqueue(v)
	}
}
