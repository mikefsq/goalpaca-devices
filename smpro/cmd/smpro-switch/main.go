// Command smpro-switch serves the StellarMate SM Pro board's switch side as a
// standalone ASCOM Alpaca Switch: power outputs, dew heaters, variable output,
// LEDs, antenna power, and voltage/current sensing.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
//
// The board's focuser is a separate binary (smpro-focuser) on its own port.
// The two drive subsystems that share nothing — this one the I2C expander,
// DAC, SPI ADC and dew PWM — so either runs without the other, and a single
// binary opening the whole board would contend with both.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/smpro" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("smpro-switch") }
