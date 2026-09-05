// Command onstep serves the onstep driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/onstep" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("onstep") }
