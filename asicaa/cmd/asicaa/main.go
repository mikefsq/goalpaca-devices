// Command asicaa serves a ZWO CAA camera angle adjuster as a standalone ASCOM
// Alpaca Rotator, over the goasi/caa SDK wrapper (cgo; needs the ZWO SDK).
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asicaa" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asicaa") }
