// Command asiam5 serves the asiam5 driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiam5" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiam5") }
