package driver

import (
	"path/filepath"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

// The driver is wired to the shared reject-memory: what it records is what its next dial excludes.
//
// The set semantics (dedupe, case-insensitivity, sorting, tolerating no state file) are the
// library's and are covered there. What is worth pinning here is only that this device is actually
// connected to it — the failure being a driver that probes correctly and then forgets, reopening a
// neighbour's port every reconnect for the life of the session.
func TestRejectedSerialsAreRememberedForNextDial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rst.json")
	tel := NewTelescope("")
	tel.state = registry.Spec{Driver: "rst", Instance: "rst", StatePath: path}

	if got := tel.state.RejectedSerials(); got != nil {
		t.Errorf("nothing learned yet: %v, want nil", got)
	}
	if _, err := tel.state.RememberRejected([]string{"AG0JWD3W"}); err != nil {
		t.Fatal(err)
	}
	if got := tel.state.RejectedSerials(); len(got) != 1 || got[0] != "AG0JWD3W" {
		t.Errorf("next dial would exclude %v, want [AG0JWD3W]", got)
	}
}

// A hand-run binary has no persisted file. It must still dial — it simply re-probes each start.
func TestDialWithoutState(t *testing.T) {
	tel := NewTelescope("")
	if got := tel.state.RejectedSerials(); got != nil {
		t.Errorf("with no state path = %v, want nil", got)
	}
	if added, err := tel.state.RememberRejected([]string{"AG0JWD3W"}); err != nil || added {
		t.Errorf("recording with no state path = %v, %v; want false, nil", added, err)
	}
}
