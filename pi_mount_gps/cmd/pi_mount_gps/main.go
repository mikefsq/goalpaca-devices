// Command pi_mount_gps serves the pi_mount_gps driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/pi_mount_gps" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("pi_mount_gps") }
