// Command ptpcam serves a Fujifilm or Sony stills camera as a standalone ASCOM
// Alpaca Camera, over PTP with no vendor SDK.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the setup
// form, persistence, and discovery come from the library. The driver package
// supplies the hardware knowledge and registers itself on import; importing it
// also pulls in ptp/fuji and ptp/sony, whose init functions register the
// vendors that USB enumeration then sees.
//
// -list is the one flag of its own: it prints the attached cameras and exits.
package main

import (
	"flag"
	"fmt"
	"os"

	_ "github.com/mikefsq/goalpaca-devices/ptpcam" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/ptp/usb"
)

func main() {
	var list bool
	err := devicemain.RunWith("ptpcam", devicemain.Options{
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&list, "list", false, "list attached cameras and exit")
		},
		BeforeRun: func() (bool, error) {
			if !list {
				return false, nil
			}
			return true, listCameras()
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ptpcam: "+err.Error())
		os.Exit(1)
	}
}

func listCameras() error {
	devs, err := usb.Enumerate()
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("no cameras found")
		return nil
	}
	for _, d := range devs {
		fmt.Printf("  %s\n", d)
	}
	return nil
}
