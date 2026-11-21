package driver

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	goasi "github.com/mikefsq/goasi/ccd"
)

var _ alpacadev.Reconfigurable = (*ASICamera)(nil)

func TestRegistryFastReadoutConfig(t *testing.T) {
	drv, ok := registry.Lookup("asiccd")
	if !ok {
		t.Fatal("asiccd driver not registered")
	}

	dev, err := drv.New(registry.Spec{Raw: json.RawMessage(`{"driver":"asiccd","index":0}`)})
	if err != nil {
		t.Fatalf("default config failed: %v", err)
	}
	cam, ok := dev.(*ASICamera)
	if !ok {
		t.Fatalf("expected *ASICamera, got %T", dev)
	}
	if cam.wantFastReadout {
		t.Fatal("expected wantFastReadout=false (full native ADC default)")
	}

	dev, err = drv.New(registry.Spec{Raw: json.RawMessage(`{"driver":"asiccd","fastReadout":true}`)})
	if err != nil {
		t.Fatalf("explicit fastReadout config failed: %v", err)
	}
	cam = dev.(*ASICamera)
	if !cam.wantFastReadout {
		t.Fatal("expected wantFastReadout=true")
	}
}

func TestFastReadoutGuardsWithoutHardware(t *testing.T) {
	cam := NewASICamera(0, "")
	if cam.CanFastReadout() {
		t.Fatal("expected CanFastReadout=false while disconnected")
	}
	if _, err := cam.FastReadout(); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Fatalf("FastReadout while disconnected: want ErrNotConnected, got %v", err)
	}
	if err := cam.SetFastReadout(true); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Fatalf("SetFastReadout while disconnected: want ErrNotConnected, got %v", err)
	}

	// Simulate an attached camera whose ASI_HIGH_SPEED_MODE is absent or read-only.
	cam.cam = &goasi.GoAsiCamera{}
	cam.hwPresent.Store(true)
	if cam.CanFastReadout() {
		t.Fatal("expected CanFastReadout=false when ASI_HIGH_SPEED_MODE is unsupported")
	}
	if _, err := cam.FastReadout(); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatalf("FastReadout when unsupported: want ErrNotImplemented, got %v", err)
	}
	if err := cam.SetFastReadout(true); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatalf("SetFastReadout when unsupported: want ErrNotImplemented, got %v", err)
	}
	if err := cam.Reconfigure(&Config{FastReadout: true}); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatalf("Reconfigure(true) when unsupported: want ErrNotImplemented, got %v", err)
	}
	if err := cam.Reconfigure(&Config{FastReadout: false}); err != nil {
		t.Fatalf("Reconfigure(false) when unsupported: %v", err)
	}

	// Simulate an attached camera that exposes writable ASI_HIGH_SPEED_MODE.
	cam.hasFastReadout = true
	if !cam.CanFastReadout() {
		t.Fatal("expected CanFastReadout=true when writable")
	}
	got, err := cam.FastReadout()
	if err != nil || got {
		t.Fatalf("FastReadout = (%v, %v), want (false, nil)", got, err)
	}

	cam.exposeOp.Begin()
	if err := cam.SetFastReadout(true); !errors.Is(err, alpacadev.ErrInvalidOperation) {
		t.Fatalf("SetFastReadout while busy: want ErrInvalidOperation, got %v", err)
	}
}

func TestReconfigureFastReadoutWithoutHardware(t *testing.T) {
	cam := NewASICamera(0, "")
	if err := cam.Reconfigure("wrong-type"); err == nil {
		t.Fatal("expected error for wrong config type")
	}
	if err := cam.Reconfigure(&Config{FastReadout: true}); err != nil {
		t.Fatalf("Reconfigure(true) while disconnected: %v", err)
	}
	if !cam.wantFastReadout {
		t.Fatal("expected wantFastReadout=true after Reconfigure")
	}
	if err := cam.Reconfigure(&Config{FastReadout: false}); err != nil {
		t.Fatalf("Reconfigure(false) while disconnected: %v", err)
	}
	if cam.wantFastReadout {
		t.Fatal("expected wantFastReadout=false after Reconfigure")
	}

	cam.exposeOp.Begin()
	if err := cam.Reconfigure(&Config{FastReadout: true}); !errors.Is(err, alpacadev.ErrInvalidOperation) {
		t.Fatalf("Reconfigure while busy: want ErrInvalidOperation, got %v", err)
	}
}
