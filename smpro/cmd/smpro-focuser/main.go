// Command smpro-focuser serves the StellarMate SM Pro board's TMC2209 stepper
// as a standalone ASCOM Alpaca Focuser.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
//
// The board's switch is a separate binary (smpro-switch) on its own port. The
// two drive subsystems that share nothing — this one the TMC2209 on its own
// UART — so either runs without the other, and a single binary opening the
// whole board would contend with both.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/smpro" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("smpro-focuser") }
