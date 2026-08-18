package driver

import (
	"context"
	"errors"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

var _ alpacadev.Focuser = (*SMProFocuser)(nil)

// SMProFocuser is the ASCOM Focuser device over the SM Pro's TMC2209 stepper.
// The motor is open-loop (no encoder): position is tracked in microsteps by the
// stellarmate library from timed VACTUAL moves, so it presents as an absolute
// focuser whose position is the library's estimate.
type SMProFocuser struct {
	alpacadev.BaseFocuser
	hub *Hub
	session
}

// NewFocuser creates the Focuser device on a shared Hub.
func NewFocuser(hub *Hub) *SMProFocuser {
	f := &SMProFocuser{hub: hub}
	f.Version = "0.1.0"
	f.Info = "smpro — StellarMate SM Pro TMC2209 focuser Alpaca driver over Go stellarmate"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = "SMPro-focuser"
	f.DevName = "SM Pro Focuser"
	f.Desc = "StellarMate SM Pro TMC2209 focuser (open-loop)"
	return f
}

// --- Lifecycle (the board is the process's; the session is the client's) ---

func (f *SMProFocuser) Open(ctx context.Context) error { return f.hub.Open(ctx) }

func (f *SMProFocuser) Close(ctx context.Context) error {
	f.setConnected(false)
	return f.hub.Close(ctx)
}

func (f *SMProFocuser) Connect(ctx context.Context) error {
	if !f.hub.Ready() {
		return alpacadev.ErrNotConnected
	}
	f.setConnected(true)
	return nil
}

func (f *SMProFocuser) Connected() bool { return f.isConnected() }

// Disconnect ends this client's ASCOM session. It does NOT release the board and
// it does NOT stop an in-flight move: a client dropping its session is not a
// reason to abandon a focus run. (A client that wants the motor stopped has Halt.)
// See session.
func (f *SMProFocuser) Disconnect(ctx context.Context) error {
	f.setConnected(false)
	return nil
}

// board gates every operational member on the session being live.
func (f *SMProFocuser) board() (*stellarmate.Board, error) { return boardFor(&f.session, f.hub) }

// Busy rejects mutating writes while a move is in flight (Halt stays exempt).
func (f *SMProFocuser) Busy() bool { return f.IsMoving() }

// --- IFocuserV4 ---

func (f *SMProFocuser) Absolute() bool { return true }

func (f *SMProFocuser) IsMoving() bool {
	b, err := f.board()
	if err != nil {
		return false
	}
	return b.FocuserIsMoving()
}

func (f *SMProFocuser) maxStep() int {
	b, err := f.board()
	if err != nil {
		return 0
	}
	return int(b.FocuserMax())
}

func (f *SMProFocuser) MaxStep() int      { return f.maxStep() }
func (f *SMProFocuser) MaxIncrement() int { return f.maxStep() }

func (f *SMProFocuser) Position() (int, error) {
	b, err := f.board()
	if err != nil {
		return 0, err
	}
	return int(b.FocuserPosition()), nil
}

// Move starts an absolute move; IsMoving is the completion property.
func (f *SMProFocuser) Move(position int) error {
	b, err := f.board()
	if err != nil {
		return err
	}
	if position < 0 {
		position = 0
	}
	err = b.MoveFocuserAbsolute(uint32(position))
	if errors.Is(err, stellarmate.ErrBusy) {
		return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "focuser is already moving")
	}
	return err
}

func (f *SMProFocuser) Halt() error {
	b, err := f.board()
	if err != nil {
		return err
	}
	return b.AbortFocuser()
}

// StepSize (microns/step) and Temperature are unknown: the focuser hardware the
// board drives varies per rig, and the board has no temperature probe. TempComp
// stays unavailable (the BaseFocuser default).
