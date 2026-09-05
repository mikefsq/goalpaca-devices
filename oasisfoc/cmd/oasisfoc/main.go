// Command oasisfoc serves the oasisfoc driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/oasisfoc" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("oasisfoc") }
