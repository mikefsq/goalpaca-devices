// Command polemaster serves a QHY PoleMaster as an Alpaca camera.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/polemaster" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("polemaster") }
