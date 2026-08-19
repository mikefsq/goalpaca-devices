// Command asicam exposes ZWO ASI cameras as a standalone ASCOM Alpaca server using
// the goalpaca/server (alpacadev) library and the pure-Go asicam driver — no ZWO
// libASICamera2 SDK. The USB transport is IOKit (macOS) / usbfs (Linux) / WinUSB (Windows).
//
// One process serves one Alpaca port with one camera device per entry of the
// config file's "cameras" array (device 0, 1, … in array order), so every
// camera stays reachable through a single discovered server for clients that
// stop at the first one. The array defaults to two entries. -config takes
// the same device file an orchestrator keeps in devices.d, so the binary runs
// identically by hand and under `alpacahurd -launch` (which passes
// `-discovery register -config <file>`); the -serial flag remains the file-less
// way to run it, and a second copy on another port serves a second camera set.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/mikefsq/astrocam"
	_ "github.com/mikefsq/astrocam/sensors" // registers the PID -> sensor profile table (required by astrocam.Open)
	driver "github.com/mikefsq/goalpaca-devices/astrocam"
	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// cameraBlock is one entry of the file's "cameras" array: the driver's
// per-camera Config plus a display name and an enable switch. A block with no
// "serial" and no "index" binds the camera at its array position. Device
// numbers are array positions, so a disabled block leaves a hole rather than
// renumbering the blocks after it.
type cameraBlock struct {
	Name   string `json:"name,omitempty"`
	Enable *bool  `json:"enable,omitempty"` // absent or true = serve; false = skip this device number
	driver.Config
}

func (b cameraBlock) enabled() bool { return b.Enable == nil || *b.Enable }

// driverKeys returns the driver-owned keys of a device file: everything but
// the host-owned common keys (registry.CommonKeys) and "cameras" itself.
func driverKeys(entry map[string]json.RawMessage) map[string]json.RawMessage {
	common := map[string]bool{"cameras": true}
	for _, k := range registry.CommonKeys() {
		common[strings.ToLower(k)] = true
	}
	out := map[string]json.RawMessage{}
	for k, v := range entry {
		if !common[strings.ToLower(k)] {
			out[k] = v
		}
	}
	return out
}

const defaultPort = 11111

func main() {
	configPath := flag.String("config", "",
		`device file (JSON with // comments): "port", "name", and a "cameras" array, one block per `+
			`Alpaca camera device (0, 1, … in order; two empty blocks when absent). -schema commented prints it.`)
	port := flag.Int("port", 0, fmt.Sprintf("Alpaca HTTP port (default: the file's, else %d)", defaultPort))
	serial := flag.String("serial", "",
		"comma-separated factory serials (hex) — one Alpaca camera device per serial, in order; the file-less "+
			"alternative to -config. Empty with no -config = auto-enumerate all attached.")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	check := flag.Bool("check", false, "load the config, construct every camera (no hardware is touched), report, and exit")
	schema := flag.String("schema", "", "print the config schema and exit: commented (a device file with the default two-camera array)")
	flag.Parse()

	switch *schema {
	case "":
	case "commented":
		fmt.Print(commentedSchema)
		return
	default:
		log.Fatalf("asicam: -schema wants commented, got %q", *schema)
	}
	if *configPath != "" && strings.TrimSpace(*serial) != "" {
		log.Fatal(`asicam: -config and -serial are exclusive: the file's "cameras" array names the serials`)
	}

	// The instance is the device file's stem, the same word an orchestrator
	// uses, so a standalone run and an orchestrated one log the same name.
	instance := ""
	if *configPath != "" {
		instance = strings.TrimSuffix(filepath.Base(*configPath), filepath.Ext(*configPath))
	}

	// Build the camera blocks: the file's "cameras" array (two empty blocks when
	// the file names none), else the -serial list, else auto-enumeration.
	portNum := defaultPort
	displayName := ""
	var blocks []cameraBlock
	var blockKeys []map[string]json.RawMessage // keys each block set, for index defaulting and form pinning
	if *configPath != "" {
		entry, err := devicemain.ReadDeviceFile(*configPath)
		if err != nil {
			log.Fatalf("asicam: %v", err)
		}
		if v, ok := entry["port"]; ok {
			_ = json.Unmarshal(v, &portNum)
		}
		if v, ok := entry["name"]; ok {
			_ = json.Unmarshal(v, &displayName)
		}
		if v, ok := entry["cameras"]; ok {
			var raws []json.RawMessage
			if err := json.Unmarshal(v, &raws); err != nil {
				log.Fatalf(`asicam: %s: "cameras": %v`, *configPath, err)
			}
			for i, r := range raws {
				dec := json.NewDecoder(bytes.NewReader(r))
				dec.DisallowUnknownFields()
				var b cameraBlock
				if err := dec.Decode(&b); err != nil {
					log.Fatalf("asicam: %s: cameras[%d]: %v", *configPath, i, err)
				}
				var keys map[string]json.RawMessage
				_ = json.Unmarshal(r, &keys)
				blocks, blockKeys = append(blocks, b), append(blockKeys, keys)
			}
		} else if flat := driverKeys(entry); len(flat) > 0 {
			// The flat one-camera form: the entry's own driver keys are the
			// single block, so a classic per-camera device file serves one
			// camera here the same as it does compiled in.
			raw, _ := json.Marshal(flat)
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var b cameraBlock
			if err := dec.Decode(&b); err != nil {
				log.Fatalf("asicam: %s: %v", *configPath, err)
			}
			blocks, blockKeys = append(blocks, b), append(blockKeys, flat)
		}
		if len(blocks) == 0 {
			// The default: two cameras, device 0 and 1, each binding its
			// enumeration index until a "serial" pins it.
			blocks = make([]cameraBlock, 2)
			blockKeys = make([]map[string]json.RawMessage, 2)
		}
	} else if s := strings.TrimSpace(*serial); s != "" {
		for _, sn := range strings.Split(s, ",") {
			if sn = strings.TrimSpace(sn); sn != "" {
				blocks = append(blocks, cameraBlock{Config: driver.Config{Serial: sn}})
				blockKeys = append(blockKeys, map[string]json.RawMessage{"serial": nil})
			}
		}
	} else {
		devs, err := astrocam.Enumerate()
		if err == nil && len(devs) > 0 {
			log.Printf("asicam: %d ASI camera(s) attached", len(devs))
			for i, d := range devs {
				log.Printf("  device %d: %s", i, d)
				blocks = append(blocks, cameraBlock{})
				blockKeys = append(blockKeys, nil)
				_ = i
			}
		}
		if len(blocks) == 0 {
			log.Printf("asicam: no cameras to serve yet (pass -serial s1,s2 or -config to advertise devices before plug-in)")
			blocks = append(blocks, cameraBlock{})
			blockKeys = append(blockKeys, nil)
		}
	}
	if *port != 0 {
		portNum = *port
	}

	// newCam constructs device i from its block, the same way the registry
	// driver's New does compiled in: bind identity only, no hardware.
	newCam := func(i int, b cameraBlock) *driver.PureASICamera {
		idx := b.Index
		if b.Serial == "" {
			if _, has := blockKeys[i]["index"]; !has {
				idx = i // an empty block binds the camera at its array position
			}
		}
		cam := driver.NewPureASICamera(idx, b.Serial)
		if instance != "" {
			cam.Instance = instance
			if len(blocks) > 1 {
				cam.Instance = instance + "/" + strconv.Itoa(i)
			}
		}
		cam.SetFixDefects(b.FixDefects)
		if b.FpsPercent != 0 {
			cam.SetFPSPercent(b.FpsPercent)
		}
		switch {
		case b.Name != "":
			cam.DevName = b.Name
		case displayName != "" && len(blocks) == 1:
			cam.DevName = displayName
		}
		return cam
	}
	// A disabled block stays a hole: no device at its number, later blocks keep
	// theirs.
	cams := make([]*driver.PureASICamera, len(blocks))
	for i, b := range blocks {
		if b.enabled() {
			cams[i] = newCam(i, b)
		}
	}

	if *check {
		for i, cam := range cams {
			if cam == nil {
				fmt.Printf("skip   %-22s camera/%d disabled\n", "astrocam", i)
				continue
			}
			fmt.Printf("ok     %-22s camera/%d on port %d  %q\n", "astrocam", i, portNum, cam.Name())
		}
		return
	}

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		// The instance travels in the heartbeat so an orchestrator joins this
		// process to its devices.d entry.
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer, Instance: instance}
	default:
		log.Fatalf("asicam: invalid -discovery %q (want direct|register|off)", *discoveryMode)
	}
	log.Printf("asicam: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          portNum,
		Discovery:           disc,
		ServerName:          "asicam",
		Manufacturer:        "mikefsq (ZWO ASI via pure-Go asicam)",
		ManufacturerVersion: "0.1.0",
		ConfigPath:          *configPath,
		Settings:            alpacadev.NewFileStore(),
	})
	for i, cam := range cams {
		if cam == nil {
			log.Printf("asicam: camera device %d disabled in %s", i, *configPath)
			continue
		}
		if err := srv.Register(alpacadev.CameraType, i, cam); err != nil {
			log.Fatalf("asicam: register camera device %d: %v", i, err)
		}
		// The setup form is generated from the driver's tagged Config, the same
		// as under alpacahurd; keys the file (or -serial) set are the admin's
		// and render locked.
		source := "the -serial flag"
		if *configPath != "" {
			source = *configPath
		}
		raw, _ := json.Marshal(blocks[i].Config)
		pinned := map[string]bool{}
		for k := range blockKeys[i] {
			if k != "name" {
				pinned[k] = true
			}
		}
		sc, err := alpacadev.NewStructConfig(cam, func() any { return &driver.Config{} }, raw, pinned, "set in "+source)
		if err != nil {
			log.Fatalf("asicam: setup form for device %d: %v", i, err)
		}
		if err := srv.RegisterConfigurable(alpacadev.CameraType, i, sc); err != nil {
			log.Fatalf("asicam: setup form for device %d: %v", i, err)
		}
		// Persist setup-page changes per device under the state directory,
		// which the systemd device unit points at the orchestrator's.
		if instance != "" {
			stem := instance
			if i > 0 {
				stem += "." + strconv.Itoa(i)
			}
			_ = srv.SettingsPath(alpacadev.CameraType, i, filepath.Join(alpacadev.StateDir("astrocam"), "devices", stem+".json"))
		}
		// A reload re-reads the file and rebuilds this device in place (offered
		// on the setup page; an orchestrator asks for it over HTTP).
		if *configPath != "" {
			i := i
			_ = srv.SetReloader(alpacadev.CameraType, i, func(context.Context) (alpacadev.Device, alpacadev.Configurable, error) {
				entry, err := devicemain.ReadDeviceFile(*configPath)
				if err != nil {
					return nil, nil, err
				}
				var raws []json.RawMessage
				if v, ok := entry["cameras"]; ok {
					if err := json.Unmarshal(v, &raws); err != nil {
						return nil, nil, err
					}
				}
				b := cameraBlock{}
				if i < len(raws) {
					dec := json.NewDecoder(bytes.NewReader(raws[i]))
					dec.DisallowUnknownFields()
					if err := dec.Decode(&b); err != nil {
						return nil, nil, fmt.Errorf("cameras[%d]: %v", i, err)
					}
					var keys map[string]json.RawMessage
					_ = json.Unmarshal(raws[i], &keys)
					blockKeys[i] = keys
				} else {
					blockKeys[i] = nil
				}
				if !b.enabled() {
					return nil, nil, fmt.Errorf("cameras[%d] is disabled in the file; a restart removes the device", i)
				}
				return newCam(i, b), nil, nil
			})
		}
		log.Printf("asicam: registered camera device %d (%s)", i, cam.ID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asicam: serving Alpaca on :%d (Ctrl-C to stop)", portNum)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asicam: %v", err)
	}
	log.Printf("asicam: shut down")
}

// commentedSchema is the -schema commented output: a device file with the
// default two-camera array. Every commented line carries its own trailing
// comma and each block ends on a live "name", so uncommenting any one line
// yields valid JSON with no other edit.
const commentedSchema = `{
  // ZWO ASI camera(s) (pure-Go USB driver): one process, one port, one Alpaca
  // camera device per "cameras" block (device 0, 1, … in order), so every
  // camera is reachable through this one server.
  // Uncomment a line to override its default.
  "driver": "astrocam",
  // "port": 11111,           // Alpaca HTTP port
  "cameras": [
    {
      // "serial": "",          // factory serial (hex); stable across replug, recommended
      // "index": 0,            // bind the Nth attached camera when no serial (default: the block's position)
      // "fixdefects": false,   // apply the factory hot-pixel map to full-frame RAW16
      // "fpsPercent": 0,       // 40..100 readout throttle; 0 keeps the link default
      // "enable": false,       // set false to skip this device number (later blocks keep theirs)
      "name": ""               // display name for device 0
    },
    {
      // "serial": "",          // factory serial (hex); stable across replug, recommended
      // "index": 1,            // bind the Nth attached camera when no serial (default: the block's position)
      // "fixdefects": false,   // apply the factory hot-pixel map to full-frame RAW16
      // "fpsPercent": 0,       // 40..100 readout throttle; 0 keeps the link default
      // "enable": false,       // set false to skip this device number (later blocks keep theirs)
      "name": ""               // display name for device 1
    }
  ],
  "enable": false            // set true to serve this device
}
`
