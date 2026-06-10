// Command asiccd exposes a ZWO ASI camera as a standalone ASCOM Alpaca
// server, using the goalpaca/server (alpacadev) library and the goasi/ccd SDK
// wrapper.
//
// The ZWO ASICamera2 shared library is NOT bundled — install it from the ZWO
// SDK and point the linker/loader at it (pick the arch matching your build):
//
//	CGO_ENABLED=1 CGO_LDFLAGS="-L/path/to/sdk/lib" go build ./...
//	LD_LIBRARY_PATH=/path/to/sdk/lib ./asiccd     # (DYLD_LIBRARY_PATH on macOS)
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	alpacadev "github.com/mikefsq/goalpaca/server"
	driver "github.com/mikefsq/asiccd-alpaca"
	goasi "github.com/mikefsq/goasi/ccd"
)

func main() {
	port := flag.Int("port", 11111, "Alpaca HTTP port")
	index := flag.Int("camera", 0, "ASI camera index (used only when -serial is empty)")
	serial := flag.String("serial", "",
		"bind the camera with this serial (hex); recommended for multi-camera and start-before-plug")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227, no proxy needed) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// Do NOT require a camera at startup: the service may be started before the
	// camera is plugged in. The driver brings the Alpaca endpoint up and acquires
	// the camera (by serial) whenever it appears.
	if n := goasi.ASIGetNumOfConnectedCameras(); n > 0 {
		log.Printf("asiccd: %d ASI camera(s) currently connected", n)
	} else {
		log.Printf("asiccd: no ASI camera yet — waiting for one to be attached")
	}

	cam := driver.NewASICamera(*index, *serial)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asiccd: invalid -discovery %q (want register|direct|off)", *discoveryMode)
	}
	log.Printf("asiccd: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asiccd",
		Manufacturer:        "mikefsq (ZWO ASI via goasi)",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.CameraType, 0, cam); err != nil {
		log.Fatalf("asiccd: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asiccd: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asiccd: %v", err)
	}
	log.Printf("asiccd: shut down")
}
