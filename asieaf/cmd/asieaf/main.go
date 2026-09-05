// Command asieaf serves the asieaf driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asieaf" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asieaf") }
