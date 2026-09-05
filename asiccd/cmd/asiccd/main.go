// Command asiccd serves the asiccd driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiccd" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiccd") }
