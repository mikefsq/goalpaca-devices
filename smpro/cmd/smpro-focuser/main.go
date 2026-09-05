// Command smpro-focuser serves the smpro-focuser driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/smpro" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("smpro-focuser") }
