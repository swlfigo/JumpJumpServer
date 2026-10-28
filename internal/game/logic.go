// Package game is a bit-exact port of the client's JumpJump/GameLogic.swift.
// The server uses it as the single authority on landings and scores.
//
// All math is float32, like the client. Go may fuse x*y+z into one FMA
// instruction (arm64 does), which Swift never does, so every product that
// feeds an addition is wrapped in float32(): the Go spec guarantees an
// explicit conversion rounds and therefore prevents fusion.
package game

import "math"

type Vec2 struct{ X, Z float32 }
type Vec3 struct{ X, Y, Z float32 }

// SeededRandom mirrors the client's LCG, including abs(seed % 233_280).
type SeededRandom struct{ seed int }

func NewSeededRandom(seed int) SeededRandom {
	seed %= 233280
	if seed < 0 {
		seed = -seed
	}
	return SeededRandom{seed: seed}
}

func (r *SeededRandom) Next() float32 {
	r.seed = (9301*r.seed + 49297) % 233280
	return float32(int(float32(r.seed)/233280*100)) / 100
}

type Style int

const (
	Mint Style = iota
	Charcoal
	Tiled
	Parcel
	Cylinder
)

func (s Style) IsRound() bool { return s == Cylinder }

type Platform struct {
	ID     int
	Center Vec2 // world x,z
	Radius float32
	Style  Style
}

// FirstPlatform is where every run starts.
var FirstPlatform = Platform{ID: 0, Radius: 0.5, Style: Mint}

func (p Platform) Contains(point Vec2) bool {
	dx, dz := point.X-p.Center.X, point.Z-p.Center.Z
	if p.Style.IsRound() {
		return lengthSquared(dx, dz) <= p.Radius*p.Radius
	}
	return abs32(dx) <= p.Radius && abs32(dz) <= p.Radius
}

type PlatformGenerator struct {
	random   SeededRandom
	sequence int
}

func NewPlatformGenerator(seed int) *PlatformGenerator {
	return &PlatformGenerator{random: NewSeededRandom(seed)}
}

// Next consumes random numbers in exactly the client's order; the style draw
// is skipped on every fifth platform.
func (g *PlatformGenerator) Next(current Platform) Platform {
	g.sequence++
	scale := 0.8 + float32(g.random.Next()*0.2)
	radius := float32(math.Round(float64(scale*5*100))) / 1000
	// Gap is EDGE-to-EDGE, not center-to-center.
	maxGap := float32(1.7)
	if g.sequence < 3 {
		maxGap = 0.9
	}
	gap := 0.1 + float32(g.random.Next()*(maxGap-0.1))
	straight := g.random.Next() > 0.5
	distance := current.Radius + gap + radius
	offset := Vec2{0, -distance}
	if straight {
		offset = Vec2{distance, 0}
	}
	style := Parcel
	if g.sequence%5 != 0 {
		style = Style(int(g.random.Next() * 5))
	}
	center := Vec2{current.Center.X + offset.X, current.Center.Z + offset.Z}
	return Platform{ID: g.sequence, Center: center, Radius: radius, Style: style}
}

const Gravity float32 = 72

type Trajectory struct {
	Start           Vec3
	Direction       Vec2
	HorizontalSpeed float32
	VerticalSpeed   float32
	Duration        float32
}

func NewTrajectory(start Vec3, target Vec2, pressSeconds float32) Trajectory {
	dx, dz := target.X-start.X, target.Z-start.Z
	direction := Vec2{1, 0}
	if lengthSquared(dx, dz) > 0.000001 {
		length := sqrt32(lengthSquared(dx, dz))
		direction = Vec2{dx / length, dz / length}
	}
	press := swiftMax(0, swiftMin(pressSeconds, 3))
	horizontal := swiftMin(press*7, 15)
	vertical := swiftMin(13.5+float32(press*1.5), 18)
	// Positive root of start.y + vy*t - g*t*t/2 = 0 (platform top y=0).
	duration := (vertical + sqrt32(float32(vertical*vertical)+float32(2*Gravity*swiftMax(start.Y, 0)))) / Gravity
	return Trajectory{Start: start, Direction: direction, HorizontalSpeed: horizontal, VerticalSpeed: vertical, Duration: duration}
}

func (t Trajectory) Position(time float32) Vec3 {
	hx := float32(float32(t.Direction.X*t.HorizontalSpeed) * time)
	hz := float32(float32(t.Direction.Z*t.HorizontalSpeed) * time)
	y := float32(t.VerticalSpeed*time) - float32(float32(0.5*Gravity*time)*time)
	return Vec3{t.Start.X + hx, t.Start.Y + y, t.Start.Z + hz}
}

func (t Trajectory) Landing() Vec2 {
	p := t.Position(t.Duration)
	return Vec2{p.X, p.Z}
}

type LandingResult int

const (
	Perfect LandingResult = iota
	Success
	Stayed
	Miss
)

func (r LandingResult) String() string {
	return [...]string{"perfect", "success", "stayed", "miss"}[r]
}

func (t Trajectory) Hit(current, next Platform) LandingResult {
	landing := t.Landing()
	if next.Contains(landing) {
		if lengthSquared(landing.X-next.Center.X, landing.Z-next.Center.Z) < 0.005 {
			return Perfect
		}
		return Success
	}
	if current.Contains(landing) {
		return Stayed
	}
	return Miss
}

// PressSeconds converts the protocol's integer milliseconds the same way the client does.
func PressSeconds(ms int) float32 { return float32(ms) / 1000 }

func lengthSquared(x, z float32) float32 { return float32(x*x) + float32(z*z) }

// sqrt in float64 then rounded is the correctly rounded float32 sqrt, like the client's.
func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }

func abs32(x float32) float32 { return math.Float32frombits(math.Float32bits(x) &^ (1 << 31)) }

// Swift.min/max semantics (y < x ? y : x and y >= x ? y : x), which differ
// from Go's builtins for NaN and signed zero.
func swiftMin(x, y float32) float32 {
	if y < x {
		return y
	}
	return x
}

func swiftMax(x, y float32) float32 {
	if y >= x {
		return y
	}
	return x
}
