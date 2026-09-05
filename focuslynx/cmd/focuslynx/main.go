// Command focuslynx serves the focuslynx driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/focuslynx" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("focuslynx") }
