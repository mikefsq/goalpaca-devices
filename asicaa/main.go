// Command asicaa exposes a ZWO CAA (Camera Angle Adjuster) as a standalone ASCOM
// Alpaca rotator server, using the goalpaca/server (alpacadev) library and the
// goasi/caa SDK wrapper.
//
// The ZWO CAA shared library is NOT bundled — install it from the ZWO SDK and
// point the linker/loader at it (pick the arch matching your build):
//
//	CGO_ENABLED=1 CGO_LDFLAGS="-L/path/to/sdk/lib" go build ./...
//	LD_LIBRARY_PATH=/path/to/sdk/lib ./asicaa     # (DYLD_LIBRARY_PATH on macOS)
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/caa"
)

func main() {
	port := flag.Int("port", 11114, "Alpaca HTTP port")
	index := flag.Int("rotator", 0, "CAA rotator index (used only when -serial is empty)")
	serial := flag.String("serial", "",
		"bind the rotator with this serial (hex); recommended for multi-device and start-before-plug")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227, no proxy needed) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// Do NOT require a rotator at startup: the service may be started before the
	// device is plugged in. The driver brings the Alpaca endpoint up and acquires
	// the rotator (by serial) whenever it appears.
	if n := caa.GetNum(); n > 0 {
		log.Printf("asicaa: %d CAA rotator(s) currently connected", n)
	} else {
		log.Printf("asicaa: no CAA rotator yet — waiting for one to be attached")
	}

	rot := NewASIRotator(*index, *serial)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asicaa: invalid -discovery %q (want register|direct|off)", *discoveryMode)
	}
	log.Printf("asicaa: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asicaa",
		Manufacturer:        "mikefsq (ZWO CAA via goasi)",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.RotatorType, 0, rot); err != nil {
		log.Fatalf("asicaa: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asicaa: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asicaa: %v", err)
	}
	log.Printf("asicaa: shut down")
}
