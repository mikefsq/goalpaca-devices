// Command asieaf exposes a ZWO EAF focuser as a standalone ASCOM Alpaca server,
// using the goalpaca/server (alpacadev) library and the goasi/eaf SDK wrapper.
//
// The ZWO EAFFocuser shared library is NOT bundled — install it from the ZWO SDK
// and point the linker/loader at it (pick the arch matching your build). On Linux
// the SDK also needs libsdbus-c++.so.2 and libWrapperSdbus.so from the same dir:
//
//	CGO_ENABLED=1 CGO_LDFLAGS="-L/path/to/sdk/lib" go build ./...
//	LD_LIBRARY_PATH=/path/to/sdk/lib ./asieaf     # (DYLD_LIBRARY_PATH on macOS)
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/eaf"
)

func main() {
	port := flag.Int("port", 11112, "Alpaca HTTP port")
	index := flag.Int("focuser", 0, "EAF focuser index (used only when -serial is empty)")
	serial := flag.String("serial", "",
		"bind the focuser with this serial (hex); recommended for multi-device and start-before-plug")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227, no proxy needed) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// Do NOT require a focuser at startup: the service may be started before the
	// device is plugged in. The driver brings the Alpaca endpoint up and acquires
	// the focuser (by serial) whenever it appears.
	if n := eaf.GetNum(); n > 0 {
		log.Printf("asieaf: %d EAF focuser(s) currently connected", n)
	} else {
		log.Printf("asieaf: no EAF focuser yet — waiting for one to be attached")
	}

	foc := NewASIFocuser(*index, *serial)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asieaf: invalid -discovery %q (want register|direct|off)", *discoveryMode)
	}
	log.Printf("asieaf: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asieaf",
		Manufacturer:        "mikefsq (ZWO EAF via goasi)",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, foc); err != nil {
		log.Fatalf("asieaf: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asieaf: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asieaf: %v", err)
	}
	log.Printf("asieaf: shut down")
}
