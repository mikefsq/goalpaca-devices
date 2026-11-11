// Command scopehub serves scopehub Alpaca Switch and ObservingConditions driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/scopehub" // registers driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("scopehub") }
