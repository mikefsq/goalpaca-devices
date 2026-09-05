// Command smpro-switch serves the smpro-switch driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/smpro" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("smpro-switch") }
