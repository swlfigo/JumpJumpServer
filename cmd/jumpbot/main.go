// Command jumpbot is a practice opponent: it joins a room, readies up and
// jumps like a decent human, with a little press error, until it falls.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"github.com/coder/websocket"

	"jumpjump-server/internal/game"
)

type message struct {
	Type        string `json:"type"`
	PlayerID    int    `json:"playerID"`
	Seed        int    `json:"seed"`
	CountdownMs int    `json:"countdownMs"`
	Seq         int    `json:"seq"`
	PressMs     int    `json:"pressMs"`
	Result      string `json:"result"`
	Score       int    `json:"score"`
}

func main() {
	url := flag.String("url", "ws://127.0.0.1:7788/ws", "server WebSocket URL")
	room := flag.String("room", "1", "room code")
	name := flag.String("name", "Bot", "display name")
	jumps := flag.Int("jumps", 0, "deliberately fall after this many jumps (0 = keep going)")
	errorMs := flag.Float64("error", 40, "standard deviation of the press error in ms")
	rejoin := flag.Duration("rejoin", 0, "leave after each match and rejoin this much later (0 = stay for rematches)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	for {
		if err := session(*url, *room, *name, *jumps, *errorMs, *rejoin == 0, log); err != nil {
			log.Error("session", "err", err)
			os.Exit(1)
		}
		log.Info("left the room", "rejoin in", *rejoin)
		time.Sleep(*rejoin)
	}
}

// session joins the room and plays; it returns after one match unless the
// bot stays for rematches.
func session(url, room, name string, jumps int, errorMs float64, stay bool, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	send := func(v map[string]any) {
		data, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			log.Error("send", "err", err)
			os.Exit(1)
		}
	}
	incoming := make(chan message)
	go func() {
		defer close(incoming)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m message
			json.Unmarshal(data, &m)
			select {
			case incoming <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	send(map[string]any{"type": "join", "name": name, "roomCode": room})
	var self int
	for m := range incoming {
		switch m.Type {
		case "welcome":
			self = m.PlayerID
			send(map[string]any{"type": "ready"})
		case "start":
			log.Info("race", "seed", m.Seed)
			race(game.NewCourse(m.Seed), time.Duration(m.CountdownMs)*time.Millisecond, jumps, errorMs, self, send, incoming, log)
			if !stay {
				send(map[string]any{"type": "leave"})
				return nil
			}
			send(map[string]any{"type": "ready"}) // up for a rematch
		case "error":
			log.Warn("refused", "msg", m)
		}
	}
	return errors.New("connection closed")
}

// race plays one match and returns once the result arrives.
func race(course *game.Course, countdown time.Duration, maxJumps int, errorMs float64, self int, send func(map[string]any), incoming <-chan message, log *slog.Logger) {
	var player game.Player
	act := time.After(countdown + think())
	for seq := 1; ; {
		select {
		case m, ok := <-incoming:
			if !ok {
				os.Exit(0)
			}
			switch {
			case m.Type == "result":
				log.Info("result", "score", player.Score)
				return
			case m.Type == "jumped" && m.PlayerID == self:
				log.Info("jumped", "seq", m.Seq, "pressMs", m.PressMs, "result", m.Result, "score", m.Score)
			case m.Type == "idleWarning" || m.Type == "error":
				log.Warn("server", "msg", m)
			}
		case <-act:
			if player.Fallen {
				act = nil
				continue
			}
			press := perfectPress(course, player)
			if maxJumps > 0 && seq > maxJumps {
				press = 3000
			} else {
				press = max(0, min(3000, press+int(rand.NormFloat64()*errorMs)))
			}
			send(map[string]any{"type": "charge", "seq": seq})
			time.Sleep(time.Duration(press) * time.Millisecond)
			send(map[string]any{"type": "jump", "seq": seq, "pressMs": press})
			jump := player.Jump(course, game.PressSeconds(press))
			flight := time.Duration(float64(jump.Trajectory.Duration) * float64(time.Second))
			act = time.After(flight + 200*time.Millisecond + think())
			seq++
		}
	}
}

func think() time.Duration { return time.Duration(500+rand.IntN(700)) * time.Millisecond }

func perfectPress(course *game.Course, p game.Player) int {
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
