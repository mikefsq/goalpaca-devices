package driver

import (
	"net/http/httptest"
	"testing"

	"github.com/mikefsq/goalpaca/client"
	"github.com/mikefsq/goalpaca/conformance"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// The physics layer (shared guide sky, pointing error, seeing) wraps the base
// goalpaca/sim devices; these tests assert the wrapping hasn't broken ASCOM
// protocol conformance. The base sims pass the same ported-ConformU checks in
// goalpaca itself, so any failure here is introduced by this package.

// serveCoupledSim hosts the coupled mount + guide camera (one shared sky) on
// the real server over httptest -- the same two-device layout cmd/sim serves --
// and returns the base URL.
func serveCoupledSim(t *testing.T) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}})
	if err := srv.Register(alpacadev.TelescopeType, 0, NewMount("Sim Mount")); err != nil {
		t.Fatalf("register telescope: %v", err)
	}
	if err := srv.Register(alpacadev.CameraType, 0, NewCamera("Sim Guide Camera", 0, 0, 0, 0, 0)); err != nil {
		t.Fatalf("register camera: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestMountConformance(t *testing.T) {
	c := client.NewTelescope(serveCoupledSim(t), 0)
	conformance.CheckCommon(t, c)
	conformance.CheckTelescope(t, c)
}

func TestCameraConformance(t *testing.T) {
	c := client.NewCamera(serveCoupledSim(t), 0)
	conformance.CheckCommon(t, c)
	conformance.CheckCamera(t, c)
}
