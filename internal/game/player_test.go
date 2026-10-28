package game

import "testing"

// Scoring example from the client README: center → center → normal → center
// earns +2, +4, +1, +2; landing back on the current platform scores nothing
// and keeps the combo.
func TestScoringFollowsReadmeExample(t *testing.T) {
	var p Player
	steps := []struct {
		result LandingResult
		gain   int
	}{{Perfect, 2}, {Stayed, 0}, {Perfect, 4}, {Success, 1}, {Perfect, 2}}
	for i, step := range steps {
		before := p.Score
		p.land(step.result, Vec2{})
		if gain := p.Score - before; gain != step.gain {
			t.Fatalf("step %d (%s): gained %d, want %d", i, step.result, gain, step.gain)
		}
	}
	if p.Index != 4 || p.Fallen {
		t.Fatalf("index %d fallen %v, want 4 false", p.Index, p.Fallen)
	}
}

func TestZeroPressStaysAndFullPressFalls(t *testing.T) {
	course := NewCourse(1337)
	var p Player
	if jump := p.Jump(course, PressSeconds(0)); jump.Result != Stayed || p.Index != 0 {
		t.Fatalf("0ms press: %s index %d, want stayed on 0", jump.Result, p.Index)
	}
	if jump := p.Jump(course, PressSeconds(3000)); jump.Result != Miss || !p.Fallen {
		t.Fatalf("3000ms press: %s fallen %v, want miss", jump.Result, p.Fallen)
	}
}
