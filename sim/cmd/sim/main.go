// Command sim serves the sim driver.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/goalpaca-devices/sim"
	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11110, "Alpaca HTTP port (telescope 0 + camera 0)")
	mountName := flag.String("mount-name", "Sim Mount", "telescope device name")
	camName := flag.String("camera-name", "Sim Guide Camera", "camera device name")
	focalLen := flag.Float64("focal-length", 0, "guide scope focal length, mm (0 = built-in default)")
	pixelSize := flag.Float64("pixel-size", 0, "camera pixel size, microns, square (0 = built-in default)")
	width := flag.Int("width", 0, "sensor width, pixels (0 = built-in default)")
	height := flag.Int("height", 0, "sensor height, pixels (0 = built-in default)")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	verbose := flag.Bool("v", false, "log every Alpaca request")
	discover := flag.Bool("discover", false, "report hardware discovery support as JSON and exit")
	flag.Parse()
	if *discover {
		if err := devicemain.Discover(context.Background(), registry.Driver{Name: "sim"}, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	var reqLog *log.Logger
	if *verbose {
		reqLog = log.Default()
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:   *port,
		Discovery:    discovery(*dmode, *dsrv, *ipv6),
		ServerName:   "goalpaca-devices sim",
		Manufacturer: "mikefsq",
		Location:     "Simulated",
		Logger:       reqLog,
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, driver.NewMount(*mountName)); err != nil {
		log.Fatalf("sim: register telescope: %v", err)
	}
	cam := driver.NewCamera(*camName, *focalLen, *pixelSize, *pixelSize, *width, *height)
	if err := srv.Register(alpacadev.CameraType, 0, cam); err != nil {
		log.Fatalf("sim: register camera: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("sim: serving %q (telescope/0) + %q (camera/0), one shared sky, on :%d", *mountName, *camName, *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("sim: %v", err)
	}
}

func discovery(mode, server string, ipv6 bool) alpacadev.DiscoveryConfig {
	switch strings.ToLower(mode) {
	case "off":
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: server}
	default:
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: ipv6}
	}
}
