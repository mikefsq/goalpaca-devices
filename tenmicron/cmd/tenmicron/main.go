// Command tenmicron serves a 10Micron GM-series mount (TCP) as a standalone
// ASCOM Alpaca Telescope.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge, registers itself on import, and
// carries its own LX200 front-end: an entry setting "lx200Port" serves a
// Meade-LX200 TCP bridge over the same live mount, here and compiled in.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/tenmicron" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("tenmicron") }
