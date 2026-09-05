// Command asicaa serves the asicaa driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asicaa" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asicaa") }
