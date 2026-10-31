//go:build linux

// The driver drives Raspberry Pi hardware, so the command builds only for Linux.

// Command asiair serves the asiair driver.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiair" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiair-switch") }
