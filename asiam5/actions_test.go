package driver

import (
	"encoding/json"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/am5"
)

// The action surface is advertised in CamelCase and dispatched case-insensitively, and every name
// it advertises must actually dispatch — an advertised action that returns NotImplemented is
// worse than one that was never listed.
func TestSupportedActionsAllDispatch(t *testing.T) {
	tel := NewTelescope("", "", 0) // not connected
	names := SupportedNames(tel)
	if len(names) == 0 {
		t.Fatal("no actions advertised")
	}
	for _, n := range names {
		if n != strings.TrimSpace(n) || n == "" {
			t.Errorf("action name %q is not clean", n)
		}
		if strings.ToLower(n) == n {
			t.Errorf("action %q is advertised lower-case; the convention is CamelCase", n)
		}
		// Disconnected, so every one of them should report NotConnected rather than
		// NotImplemented: the name resolved, the mount was simply absent.
		_, err := tel.Action(n, "")
		if err == alpacadev.ErrActionNotImplemented {
			t.Errorf("advertised action %q does not dispatch", n)
		}
	}
	// ... and the same names in another case must resolve identically.
	for _, n := range names {
		if _, err := tel.Action(strings.ToUpper(n), ""); err == alpacadev.ErrActionNotImplemented {
			t.Errorf("action %q does not match case-insensitively", n)
		}
	}
}

// SupportedNames is a test shim so the list is read the same way a client would.
func SupportedNames(t *Telescope) []string { return t.SupportedActions() }

func TestUnknownActionIsNotImplemented(t *testing.T) {
	tel := NewTelescope("", "", 0)
	if _, err := tel.Action("NoSuchThing", ""); err != alpacadev.ErrActionNotImplemented {
		t.Errorf("unknown action returned %v, want ErrActionNotImplemented", err)
	}
}

// Reading the mount mode is AlignmentMode's job, so the action rejects an empty params and says
// where to look instead. A client should not need a vendor action name for a question ASCOM
// already answers.
func TestMountModeIsWriteOnlyAndPointsAtAlignmentMode(t *testing.T) {
	tel := NewTelescope("", "", 0)
	_, err := tel.Action("MountMode", "")
	if err == nil {
		t.Fatal("MountMode with no value succeeded; it should refuse and name AlignmentMode")
	}
	if !strings.Contains(err.Error(), "AlignmentMode") {
		t.Errorf("error %q does not point at the standard property", err)
	}
	if _, err := tel.Action("MountMode", "sideways"); err == nil {
		t.Error("MountMode accepted a value that is neither mode")
	}
}

// A disconnected mount is ErrNotConnected, never a silent zero: these actions all reach hardware.
func TestActionsRequireAConnectedMount(t *testing.T) {
	tel := NewTelescope("", "", 0)
	for _, n := range []string{"MAC", "Buzzer", "HeavyDuty", "MeridianFlip", "TrackingRateIndex", "SetHome", "ClearAlignment"} {
		if _, err := tel.Action(n, ""); err != alpacadev.ErrNotConnected {
			t.Errorf("%s disconnected returned %v, want ErrNotConnected", n, err)
		}
	}
}

// Read-only actions reject a value rather than ignoring it, which is what tells a caller their
// write went nowhere.
func TestReadOnlyActionsRejectAValue(t *testing.T) {
	tel := NewTelescope("", "", 0)
	for _, n := range []string{"MAC", "TrackingRateIndex"} {
		if _, err := tel.Action(n, "5"); err == nil || err == alpacadev.ErrNotConnected {
			t.Errorf("%s accepted a value (err=%v)", n, err)
		}
	}
	for _, n := range []string{"SetHome", "ClearAlignment"} {
		if _, err := tel.Action(n, "now"); err == nil || err == alpacadev.ErrNotConnected {
			t.Errorf("%s accepted a value (err=%v)", n, err)
		}
	}
}

// The MeridianFlip payload round-trips as the JSON the action documents.
func TestMeridianFlipPayloadShape(t *testing.T) {
	b, err := json.Marshal(am5.MeridianFlip{Enabled: true, TrackPast: false, LimitDeg: -5})
	if err != nil {
		t.Fatal(err)
	}
	var got am5.MeridianFlip
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.TrackPast || got.LimitDeg != -5 {
		t.Errorf("round-trip = %+v", got)
	}
}
