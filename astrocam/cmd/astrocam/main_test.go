package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaCommandHelper(t *testing.T) {
	mode := os.Getenv("ASTROCAM_TEST_SCHEMA")
	if mode == "" {
		return
	}
	os.Args = []string{"astrocam", "-schema", mode, "-config", "/does/not/exist"}
	if strings.HasPrefix(mode, "check-") {
		text := `{"driver":"astrocam","serial":"ABC","enable":false}`
		if mode == "check-array" {
			text = `{"driver":"astrocam","cameras":[{"serial":"ABC"},{"serial":"DEF"}],"enable":false}`
		}
		path := filepath.Join(t.TempDir(), "camera.json")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		os.Args = []string{"astrocam", "-config", path, "-check"}
		if mode == "check-default" {
			os.Args = []string{"astrocam", "-check"}
		}
	}
	flag.CommandLine = flag.NewFlagSet("astrocam", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestSchemaCommandsDoNotRequireHardwareOrConfig(t *testing.T) {
	for _, mode := range []string{"json", "commented"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSchemaCommandHelper$")
			cmd.Env = append(os.Environ(), "ASTROCAM_TEST_SCHEMA="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			if mode == "commented" {
				if !strings.Contains(string(output), `"driver": "astrocam"`) || strings.Contains(string(output), `"cameras"`) {
					t.Fatal(string(output))
				}
				return
			}
			var schema struct {
				Driver string
				Fields []struct{ Name string }
			}
			if err := json.Unmarshal(output, &schema); err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, field := range schema.Fields {
				names[field.Name] = true
			}
			if schema.Driver != "astrocam" || !names["serial"] || !names["index"] {
				t.Fatalf("%s", output)
			}
		})
	}
}

func TestCheckSupportsFlatAndLegacyArrays(t *testing.T) {
	for _, mode := range []string{"check-flat", "check-array", "check-default"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSchemaCommandHelper$")
			cmd.Env = append(os.Environ(), "ASTROCAM_TEST_SCHEMA="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if !strings.Contains(string(out), "camera/0") || strings.Contains(string(out), "camera/1") != (mode == "check-array") {
				t.Fatal(string(out))
			}
			if strings.Contains(string(out), "attached") || strings.Contains(string(out), "no cameras to serve") {
				t.Fatal("validation attempted enumeration")
			}
		})
	}
}
