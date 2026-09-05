// Command oasisfw serves the oasisfw driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/oasisfw" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("oasisfw") }
