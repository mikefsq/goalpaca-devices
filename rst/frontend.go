package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/bridge"
)

// lx200FrontEnd is the driver's registry.Driver.FrontEnd: when the device
// entry sets "lx200Port" (which alone enables it), it serves a Meade-LX200
// TCP bridge (Stellarium, SkySafari) over the same live mount, in whichever
// process hosts the driver. One listener per host the Alpaca server binds, so
// a "listen" restriction covers LX200 too; the bridges end when ctx does,
// which the host cancels when the device is disabled. The bridge resolves the
// device per command, so a reload's device swap is followed, and a nil device
// (mid-swap) answers as not connected.
func lx200FrontEnd(mountType byte, product string) func(context.Context, func() alpacadev.Device, json.RawMessage, []string) error {
	return func(ctx context.Context, dev func() alpacadev.Device, entry json.RawMessage, hosts []string) error {
		var e struct {
			LX200Port int `json:"lx200Port"`
		}
		_ = json.Unmarshal(entry, &e)
		if e.LX200Port == 0 {
			return nil
		}
		live := func() (lx200.Mount, error) {
			lm, ok := dev().(interface{ LiveMount() (lx200.Mount, error) })
			if !ok {
				return nil, fmt.Errorf("rst: device is not a mount")
			}
			return lm.LiveMount()
		}
		addrs := []string{fmt.Sprintf(":%d", e.LX200Port)}
		if len(hosts) > 0 {
			addrs = addrs[:0]
			for _, h := range hosts {
				addrs = append(addrs, net.JoinHostPort(h, strconv.Itoa(e.LX200Port)))
			}
		}
		for _, addr := range addrs {
			srv := bridge.New(addr, live,
				bridge.WithMountType(mountType), bridge.WithIdent("RainbowAstro", "rst"), bridge.WithLogger(log.Printf))
			addr := addr
			go func() {
				log.Printf("rst: LX200 bridge on %s", addr)
				if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
					log.Printf("rst: lx200 bridge: %v", err)
				}
			}()
		}
		return nil
	}
}
