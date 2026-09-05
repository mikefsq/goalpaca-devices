// Command asiefw serves the asiefw driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiefw" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiefw") }
