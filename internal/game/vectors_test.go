package game

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

// testdata/swift_vectors.json is produced by scripts/gen-swift-vectors.sh from
// the client's Swift sources. Floats are IEEE-754 bit patterns, compared exactly.

type platformJSON struct {
	ID     int      `json:"id"`
	Center []uint32 `json:"center"`
	Radius uint32   `json:"radius"`
	Style  int      `json:"style"`
}

func (p platformJSON) platform() Platform {
	return Platform{ID: p.ID, Center: vec2(p.Center), Radius: math.Float32frombits(p.Radius), Style: Style(p.Style)}
}

type vectors struct {
	Generator []struct {
		Seed      int            `json:"seed"`
		Platforms []platformJSON `json:"platforms"`
	} `json:"generator"`
	Contains []struct {
		Platform platformJSON `json:"platform"`
		Point    []uint32     `json:"point"`
		Contains bool         `json:"contains"`
	} `json:"contains"`
	Trajectories []struct {
		Start           []uint32     `json:"start"`
		Target          []uint32     `json:"target"`
		Press           uint32       `json:"press"`
		Direction       []uint32     `json:"direction"`
		HorizontalSpeed uint32       `json:"horizontalSpeed"`
		VerticalSpeed   uint32       `json:"verticalSpeed"`
		Duration        uint32       `json:"duration"`
		Landing         []uint32     `json:"landing"`
		Current         platformJSON `json:"current"`
		Next            platformJSON `json:"next"`
		Result          string       `json:"result"`
	} `json:"trajectories"`
	Games []struct {
		Seed  int `json:"seed"`
		Steps []struct {
			PressMs int      `json:"pressMs"`
			Landing []uint32 `json:"landing"`
			Result  string   `json:"result"`
			Score   int      `json:"score"`
			Combo   int      `json:"combo"`
			Index   int      `json:"index"`
		} `json:"steps"`
	} `json:"games"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("testdata/swift_vectors.json")
	if err != nil {
		t.Fatalf("read vectors (run scripts/gen-swift-vectors.sh): %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

func vec2(b []uint32) Vec2 { return Vec2{math.Float32frombits(b[0]), math.Float32frombits(b[1])} }
func vec3(b []uint32) Vec3 {
	return Vec3{math.Float32frombits(b[0]), math.Float32frombits(b[1]), math.Float32frombits(b[2])}
}

// mismatches collects per-field failures so one run shows the whole picture.
type mismatches map[string][]string

func (m mismatches) check(field string, got, want float32, context string) {
	if math.Float32bits(got) != math.Float32bits(want) {
		m[field] = append(m[field], fmt.Sprintf("%s: got %v want %v", context, got, want))
	}
}

func (m mismatches) report(t *testing.T, total int) {
	t.Helper()
	for field, list := range m {
		t.Errorf("%s: %d/%d cases differ, e.g. %v", field, len(list), total, list[:min(3, len(list))])
	}
}

func TestGeneratorMatchesSwift(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Generator {
		course := NewCourse(c.Seed)
		for i, want := range c.Platforms {
			if got := course.Platform(i); got != want.platform() {
				t.Fatalf("seed %d platform %d: got %+v want %+v", c.Seed, i, got, want.platform())
			}
		}
	}
}

func TestContainsMatchesSwift(t *testing.T) {
	v := loadVectors(t)
	for i, c := range v.Contains {
		if got := c.Platform.platform().Contains(vec2(c.Point)); got != c.Contains {
			t.Errorf("case %d: %+v contains %v: got %v want %v", i, c.Platform.platform(), vec2(c.Point), got, c.Contains)
		}
	}
}

func TestTrajectoryMatchesSwift(t *testing.T) {
	v := loadVectors(t)
	diff := mismatches{}
	for i, c := range v.Trajectories {
		path := NewTrajectory(vec3(c.Start), vec2(c.Target), math.Float32frombits(c.Press))
		ctx := fmt.Sprintf("#%d", i)
		want := vec2(c.Direction)
		diff.check("direction.x", path.Direction.X, want.X, ctx)
		diff.check("direction.z", path.Direction.Z, want.Z, ctx)
		diff.check("horizontalSpeed", path.HorizontalSpeed, math.Float32frombits(c.HorizontalSpeed), ctx)
		diff.check("verticalSpeed", path.VerticalSpeed, math.Float32frombits(c.VerticalSpeed), ctx)
		diff.check("duration", path.Duration, math.Float32frombits(c.Duration), ctx)
		landing, wantLanding := path.Landing(), vec2(c.Landing)
		diff.check("landing.x", landing.X, wantLanding.X, ctx)
		diff.check("landing.z", landing.Z, wantLanding.Z, ctx)
		if got := path.Hit(c.Current.platform(), c.Next.platform()).String(); got != c.Result {
			diff["result"] = append(diff["result"], fmt.Sprintf("%s: got %s want %s", ctx, got, c.Result))
		}
	}
	diff.report(t, len(v.Trajectories))
}

func TestGamesMatchSwift(t *testing.T) {
	v := loadVectors(t)
	for _, g := range v.Games {
		course := NewCourse(g.Seed)
		var player Player
		for i, step := range g.Steps {
			jump := player.Jump(course, PressSeconds(step.PressMs))
			want := vec2(step.Landing)
			if math.Float32bits(jump.Landing.X) != math.Float32bits(want.X) || math.Float32bits(jump.Landing.Z) != math.Float32bits(want.Z) ||
				jump.Result.String() != step.Result || player.Score != step.Score || player.Combo != step.Combo || player.Index != step.Index {
				t.Fatalf("seed %d step %d (press %dms): got landing %v %s score %d combo %d index %d, want %v %s %d %d %d",
					g.Seed, i, step.PressMs, jump.Landing, jump.Result, player.Score, player.Combo, player.Index,
					want, step.Result, step.Score, step.Combo, step.Index)
			}
		}
	}
}
