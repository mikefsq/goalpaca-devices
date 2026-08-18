// Command focuslynx serves one Optec FocusLynx channel as a standalone ASCOM
// Alpaca Focuser.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/focuslynx" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("focuslynx") }
