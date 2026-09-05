// Command ptpcam serves the ptpcam driver.
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
