package driver

import (
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// Two different lifetimes run through this driver, and conflating them is a bug
// we had: the board's Connected state was reported as "is the hardware open?",
// which meant Connected stayed true forever after a client disconnected.
//
//   - The BOARD is hardware. The Hub opens it once at Hardware startup and
//     releases it once at teardown. It is shared by the Switch and the Focuser,
//     because two Boards on one bus would double-drive the I2C expander and the
//     stepper UART. It is owned by the PROCESS.
//
//   - A SESSION is one ASCOM client's logical connection to ONE device. Connect
//     and Disconnect flip it; operational members fault with NotConnected while it
//     is false. ASCOM requires Connected to go false after Disconnect — but it does
//     NOT require the hardware to be released, and releasing it would be wrong:
//     Disconnect must not cut the power rails, stop the focuser, or drop the dew
//     heaters just because a client closed its session.
//
// The Switch and the Focuser get a session each, not one between them: a client
// may perfectly well connect to the focuser and leave the switch alone.
type session struct {
	mu        sync.Mutex
	connected bool
}

func (s *session) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}

func (s *session) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// boardFor is the gate every operational member goes through: NotConnected unless
// this client's session is live AND the hardware is open.
func boardFor(s *session, hub *Hub) (*stellarmate.Board, error) {
	if !s.isConnected() {
		return nil, alpacadev.ErrNotConnected
	}
	return hub.Board()
}
