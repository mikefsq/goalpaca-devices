// Command asiair runs the ZWO ASIAIR power board as a standalone Alpaca server:
// one Switch device carrying the four power ports (two dimmable), the DSLR
// shutter, the auto-dew enables and the per-port telemetry.
//
// The board's on/off ports are held by GPIO character-device line requests, which
// the kernel releases the moment the process exits — reverting the lines and
// switching those ports off. So this server is the owner of the power state, and
// it must keep running for as long as the ports are meant to stay on. Stopping it
// cuts ports 3 and 4. (The dimmable ports go through sysfs PWM and survive, which
// is the asymmetry to keep in mind: killing the server would cut the mount and
// leave the dew heater running.)
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mikefsq/asiair"
	driver "github.com/mikefsq/goalpaca-devices/asiair"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11131, "Alpaca HTTP port")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")

	cfg := asiair.DefaultConfig()
	ports24 := false
	flag.StringVar(&cfg.I2CBus, "i2c", cfg.I2CBus, "telemetry I2C bus (find it with asiair's adsscan)")
	flag.StringVar(&cfg.GPIOChip, "gpiochip", cfg.GPIOChip, "GPIO character device")
	flag.IntVar(&cfg.PWMChip, "pwmchip", cfg.PWMChip, "sysfs pwmchip for the dimmable ports")
	flag.BoolVar(&ports24, "ports24", false, "use the ports 2+4 PWM pairing instead of 1+2")
	flag.Parse()

	if ports24 {
		cfg = cfg.WithPWMPorts24()
	}

	hub := driver.NewHub(cfg)
	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:   *port,
		Discovery:    discovery(*dmode, *dsrv, *ipv6),
		ServerName:   "asiair",
		Manufacturer: "mikefsq",
	})
	if err := srv.Register(alpacadev.SwitchType, 0, driver.NewSwitch(hub)); err != nil {
		log.Fatalf("asiair: register switch: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("asiair: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asiair: %v", err)
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
