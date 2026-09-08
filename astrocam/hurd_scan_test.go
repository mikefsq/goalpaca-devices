package driver

import (
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

// The identity keys and the config struct are wired by string; a rename on either side would leave
// a host pinning a camera by a key the driver never reads.
func TestIdentityKeysMatchTheConfigStruct(t *testing.T) {
	d, ok := registry.Lookup("astrocam")
	if !ok {
		t.Fatal("astrocam is not registered")
	}
	if err := registry.CheckIdentity(d); err != nil {
		t.Fatal(err)
	}
	if len(d.Identity) == 0 || d.Identity[0] != "serial" {
		t.Errorf("Identity = %v; serial must lead, since it is the value that survives a replug", d.Identity)
	}
	if d.Scan == nil {
		t.Error("astrocam can enumerate its hardware, so it must declare Scan")
	}
}
