// Command tenmicron serves the tenmicron driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/tenmicron" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("tenmicron") }
