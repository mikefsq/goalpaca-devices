// Command mgpbox serves the mgpbox driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/mgpbox" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("mgpbox") }
