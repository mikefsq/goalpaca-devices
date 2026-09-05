// Command rst serves the rst driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/rst" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("rst") }
