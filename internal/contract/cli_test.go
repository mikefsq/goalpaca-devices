// Package contract checks the executable interface used by alpacahurd, including
// custom launchers. Schema/help commands must work without configuration or hardware.
package contract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
)

func TestDriverCLI(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mains, err := filepath.Glob(filepath.Join(root, "*", "cmd", "*", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, main := range mains {
		name := filepath.Base(filepath.Dir(main))
		// sim is a multi-device development server; roiprobe is a diagnostic tool.
		if name == "sim" || name == "roiprobe" {
			continue
		}
		if (name == "asiccd" || name == "asicaa") && os.Getenv("ALPACA_TEST_SDK") != "1" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS != "linux" && (name == "asiair" || strings.HasPrefix(name, "smpro-")) {
				t.Skip("Linux-only driver registration; exercised in Linux CI")
			}
			exe := filepath.Join(t.TempDir(), name)
			cmd := exec.Command("go", "build", "-o", exe, filepath.Dir(main))
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			run := func(args ...string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, args...)
				return cmd.CombinedOutput()
			}
			help, err := run("-h")
			if err != nil && !strings.Contains(string(help), "flag: help requested") {
				t.Fatalf("help: %v %s", err, help)
			}
			for _, flag := range []string{"-schema", "-discover", "-check", "-config"} {
				if !strings.Contains(string(help), flag) {
					t.Fatalf("missing %s", flag)
				}
			}
			raw, err := run("-schema", "json", "-config", "/does/not/exist")
			if err != nil {
				t.Fatalf("JSON schema: %v %s", err, raw)
			}
			var schema struct {
				Driver string
				Type   string
				Fields []struct {
					Name string
					Type string
				}
			}
			if err := json.Unmarshal(raw, &schema); err != nil || schema.Driver == "" || schema.Type == "" {
				t.Fatalf("invalid schema: %v %s", err, raw)
			}
			raw, err = run("-schema", "commented", "-config", "/does/not/exist")
			if err != nil {
				t.Fatalf("commented schema: %v %s", err, raw)
			}
			var prototype map[string]json.RawMessage
			if err := json.Unmarshal(devicemain.StripComments(raw), &prototype); err != nil {
				t.Fatalf("invalid prototype: %v %s", err, raw)
			}
			if string(prototype["enable"]) != "false" {
				t.Fatal("prototype must be disabled")
			}
			var driver string
			_ = json.Unmarshal(prototype["driver"], &driver)
			if driver != schema.Driver {
				t.Fatalf("schema names differ: %q / %q", driver, schema.Driver)
			}
			for key := range prototype {
				if key != "driver" && key != "enable" {
					t.Fatalf("prototype field %q must be commented", key)
				}
			}
		})
	}
}
