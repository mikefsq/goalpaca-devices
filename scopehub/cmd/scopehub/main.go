// Command scopehub serves the scopehub Alpaca Switch driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/scopehub" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("scopehub") }
