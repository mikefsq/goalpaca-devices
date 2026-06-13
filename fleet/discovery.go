package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

const (
	discoveryPort    = 32227
	discoveryToken   = "alpacadiscovery" // probe prefix; a version digit follows
	discoveryV6Group = "ff12::00a1:9aca" // IPv6 discovery multicast group
)

// runDiscovery answers Alpaca discovery probes on UDP 32227 for every fleet
// device port, sending one {"AlpacaPort":N} datagram per port. The port table is
// fixed at startup (all devices are in-process and known), so there is no
// heartbeat. The socket is bound with SO_REUSEADDR/SO_REUSEPORT so it co-binds
// alongside other (non-Go / vendor) Alpaca servers answering on 32227.
func runDiscovery(ctx context.Context, ports []int, ipv6 bool) error {
	resp := make([][]byte, len(ports))
	for i, p := range ports {
		resp[i], _ = json.Marshal(struct {
			AlpacaPort int `json:"AlpacaPort"`
		}{p})
	}

	lc := net.ListenConfig{Control: alpacadev.ReuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", discoveryPort))
	if err != nil {
		return err
	}
	go serveDiscovery(ctx, pc.(*net.UDPConn), resp)

	if ipv6 {
		gaddr := &net.UDPAddr{IP: net.ParseIP(discoveryV6Group), Port: discoveryPort}
		if c6, err := net.ListenMulticastUDP("udp6", nil, gaddr); err != nil {
			log.Printf("astrofleet: discovery IPv6 join failed: %v", err)
		} else {
			go serveDiscovery(ctx, c6, resp)
		}
	}
	return nil
}

func serveDiscovery(ctx context.Context, c *net.UDPConn, resp [][]byte) {
	defer c.Close()
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if !bytes.HasPrefix(bytes.TrimSpace(buf[:n]), []byte(discoveryToken)) {
			continue
		}
		for _, b := range resp {
			_, _ = c.WriteToUDP(b, src) // one datagram per device port
		}
	}
}
