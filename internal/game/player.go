package game

// Course is the platform sequence shared by everyone in a match, generated on demand.
type Course struct {
	generator *PlatformGenerator
	platforms []Platform
}

func NewCourse(seed int) *Course {
	return &Course{generator: NewPlatformGenerator(seed), platforms: []Platform{FirstPlatform}}
}

func (c *Course) Platform(index int) Platform {
	for len(c.platforms) <= index {
		c.platforms = append(c.platforms, c.generator.Next(c.platforms[len(c.platforms)-1]))
	}
	return c.platforms[index]
}

// Player is one competitor's authoritative state, mirroring the client's
// GameRenderer.resolveLanding. Callers must not jump a fallen player.
type Player struct {
	Position Vec3
	Index    int // platform the player stands on
	Score    int
	Combo    int
	Fallen   bool
}

type Jump struct {
	Trajectory Trajectory
	Landing    Vec2
	Result     LandingResult
}

func (p *Player) Jump(course *Course, pressSeconds float32) Jump {
	path := NewTrajectory(p.Position, course.Platform(p.Index+1).Center, pressSeconds)
	jump := Jump{Trajectory: path, Landing: path.Landing(), Result: path.Hit(course.Platform(p.Index), course.Platform(p.Index+1))}
	p.land(jump.Result, jump.Landing)
	return jump
}

// land applies the scoring rules: center hits build a combo worth +2, +4, +6...;
// an off-center landing scores 1 and resets it; landing back on the current
// platform keeps both.
func (p *Player) land(result LandingResult, landing Vec2) {
	p.Position = Vec3{landing.X, 0, landing.Z}
	switch result {
	case Perfect:
		p.Combo++
		p.Score += p.Combo * 2
		p.Index++
	case Success:
		p.Combo = 0
		p.Score++
		p.Index++
	case Miss:
		p.Combo = 0
		p.Fallen = true
	}
}
