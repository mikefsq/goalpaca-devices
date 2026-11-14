package driver

import (
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	"github.com/mikefsq/goalpaca/sim"
)

// The herd panel starts open; closing it puts panel light on herd cameras.
func TestHerdPanelCoversCameras(t *testing.T) {
	drv, ok := registry.Lookup("sim-covercalibrator")
	if !ok {
		t.Fatal("sim-covercalibrator not registered")
	}
	dev, err := drv.New(registry.Spec{Raw: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	panel := dev.(*sim.CoverCalibrator)
	cam := newSimCamera("", 0, 0, 0, 0, 0)
	settle := func() {
		for panel.CoverMoving() {
			time.Sleep(10 * time.Millisecond)
		}
	}
	settle()
	if cam.SeesPanel() {
		t.Fatal("herd panel starts closed: cameras would show no sky")
	}
	_ = panel.CloseCover()
	settle()
	if !cam.SeesPanel() {
		t.Fatal("closed herd panel not seen by herd camera")
	}
	_ = panel.OpenCover()
	settle()
}
