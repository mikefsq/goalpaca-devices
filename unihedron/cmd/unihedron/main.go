// Command unihedron serves the unihedron driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/unihedron" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("unihedron") }
