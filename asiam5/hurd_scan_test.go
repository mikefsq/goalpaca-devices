package driver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

// The AM series is reachable two ways, and the config keys have to say which: the USB serial binds
// the cable, the address binds the WiFi. Either suffices, and only the first can be scanned for.
func TestIdentityAndScanAreDeclared(t *testing.T) {
	d, ok := registry.Lookup("asiam5")
	if !ok {
		t.Fatal("asiam5 is not registered")
	}
	if err := registry.CheckIdentity(d); err != nil {
		t.Fatal(err)
	}
	if len(d.Identity) != 2 || d.Identity[0] != "serial" || d.Identity[1] != "addr" {
		t.Errorf("Identity = %v; want serial then addr — a scan can fill the serial, never the address", d.Identity)
	}
	if d.Scan == nil {
		t.Error("the USB side identifies itself (ZWO VID:PID plus :GVP#), so Scan must be declared")
	}
}

// A serial is resolved through the port enumerator; a path is opened as given. Getting this
// backwards is the bug this driver shipped with — the serial was handed to am5.Open, which opens a
// device node, so a mount configured by serial tried to open /dev/<serial>.
func TestLooksLikePort(t *testing.T) {
	for _, path := range []string{"/dev/cu.usbmodem14201", "/dev/ttyACM0", "COM3", `\\.\COM12`} {
		if !looksLikePort(path) {
			t.Errorf("%q not recognised as a port", path)
		}
	}
	for _, serial := range []string{"0123456789ABCDEF", "AM5-0001", "123456", ""} {
		if looksLikePort(serial) {
			t.Errorf("%q treated as a port", serial)
		}
	}
}

// The port is its own field, defaulting to the only port ZWO's firmware serves. A host that
// already carries one is left alone, because that is what every entry written before this said
// and what an operator typing "192.168.4.1:4030" means.
func TestDialAddrJoinsHostAndPort(t *testing.T) {
	cases := []struct {
		addr string
		port int
		want string
	}{
		{"192.168.4.1", 0, "192.168.4.1:4030"},
		{"192.168.4.1", 4031, "192.168.4.1:4031"},
		{"192.168.4.1:4030", 0, "192.168.4.1:4030"},
		{"mount.local", 0, "mount.local:4030"},
	}
	for _, tc := range cases {
		tel := NewTelescope("", tc.addr, tc.port)
		if got := tel.dialAddr(); got != tc.want {
			t.Errorf("addr %q port %d = %q, want %q", tc.addr, tc.port, got, tc.want)
		}
	}
}

func TestRegistryAllowsAutomaticUSBDiscovery(t *testing.T) {
	drv, _ := registry.Lookup("asiam5")
	for _, raw := range []string{`{"driver":"asiam5"}`, `{"driver":"asiam5","serial":"","addr":"","enable":false}`} {
		dev, err := drv.New(registry.Spec{Driver: drv.Name, Raw: json.RawMessage(raw)})
		if err != nil {
			t.Fatalf("automatic discovery rejected: %v", err)
		}
		mount := dev.(*Telescope)
		if mount.serial != "" || mount.addr != "" || mount.ID == "" {
			t.Fatal("automatic mount selector was not preserved")
		}
		if !strings.Contains(mount.Description(), "automatic USB discovery") {
			t.Fatal(mount.Description())
		}
	}
	for _, raw := range []string{`{"serial":"chosen"}`, `{"addr":"mount.local"}`} {
		dev, err := drv.New(registry.Spec{Driver: drv.Name, Raw: json.RawMessage(raw)})
		if err != nil {
			t.Fatal(err)
		}
		mount := dev.(*Telescope)
		if mount.serial != "chosen" && mount.addr != "mount.local" {
			t.Fatal("explicit selection lost")
		}
	}
}
