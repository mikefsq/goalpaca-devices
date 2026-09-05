package driver

import (
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// session tracks a device’s logical connection independently of hardware ownership.
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
// the device's logical connection is live AND the hardware is open.
func boardFor(s *session, hub *Hub) (*stellarmate.Board, error) {
	if !s.isConnected() {
		return nil, alpacadev.ErrNotConnected
	}
	return hub.Board()
}
