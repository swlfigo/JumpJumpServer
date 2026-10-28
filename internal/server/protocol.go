package server

// Wire messages; see docs/protocol.md. Every message is a JSON object with a "type".

const (
	typeJoin   = "join"
	typeReady  = "ready"
	typeCharge = "charge"
	typeJump   = "jump"
	typeLeave  = "leave"
)

// clientMessage holds every field a client may send. Anything else in the
// payload (coordinates, results, scores) is ignored: the server computes those.
type clientMessage struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	RoomCode string `json:"roomCode"`
	Seq      int    `json:"seq"`
	PressMs  *int   `json:"pressMs"`
}

type playerInfo struct {
	PlayerID int    `json:"playerID"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Color    int    `json:"color"` // slot in the client's palette, unique within the room
}

type welcomeMessage struct {
	Type     string       `json:"type"`
	PlayerID int          `json:"playerID"`
	Players  []playerInfo `json:"players"`
}

type lobbyMessage struct {
	Type    string       `json:"type"`
	Players []playerInfo `json:"players"`
}

type startMessage struct {
	Type        string `json:"type"`
	Seed        int    `json:"seed"`
	CountdownMs int    `json:"countdownMs"`
}

type chargeMessage struct {
	Type     string `json:"type"`
	PlayerID int    `json:"playerID"`
	Seq      int    `json:"seq"`
}

type jumpedMessage struct {
	Type        string    `json:"type"`
	PlayerID    int       `json:"playerID"`
	Seq         int       `json:"seq"`
	PressMs     int       `json:"pressMs"`
	LandingBits [2]uint32 `json:"landingBits"` // float32 bit patterns of landing x,z
	Result      string    `json:"result"`
	Score       int       `json:"score"`
	Combo       int       `json:"combo"`
}

type readyWarningMessage struct {
	Type        string `json:"type"`
	SecondsLeft int    `json:"secondsLeft"`
}

type idleWarningMessage struct {
	Type        string `json:"type"`
	SecondsLeft int    `json:"secondsLeft"`
}

type outMessage struct {
	Type     string `json:"type"`
	PlayerID int    `json:"playerID"`
	Reason   string `json:"reason"`
}

type rankEntry struct {
	PlayerID int    `json:"playerID"`
	Name     string `json:"name"`
	Score    int    `json:"score"`
	Status   string `json:"status"`
}

type resultMessage struct {
	Type    string      `json:"type"`
	Ranking []rankEntry `json:"ranking"`
}

type errorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Why a racer is out of the match; every reason except fell is a forfeit.
const (
	reasonFell         = "fell"
	reasonIdle         = "idle"
	reasonDisconnected = "disconnected"
	reasonLeft         = "left"
)
