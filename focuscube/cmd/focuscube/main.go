// Command focuscube serves the focuscube driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/focuscube" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("focuscube") }
