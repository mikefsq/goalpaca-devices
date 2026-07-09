package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPath(t *testing.T) {
	// An explicit -config value always wins and is returned verbatim (not stat'd) —
	// so an explicit path that doesn't exist still surfaces as a LoadConfig read error.
	if got, err := resolveConfigPath("/some/explicit.json"); err != nil || got != "/some/explicit.json" {
		t.Fatalf("explicit: got %q, %v; want /some/explicit.json", got, err)
	}

	// $ASTROFLEET_CONFIG overrides the search when no explicit flag is given.
	t.Setenv("ASTROFLEET_CONFIG", "/from/env.json")
	if got, err := resolveConfigPath(""); err != nil || got != "/from/env.json" {
		t.Fatalf("env override: got %q, %v; want /from/env.json", got, err)
	}

	// With no flag and no env, the current directory's fleet.json is found first.
	t.Setenv("ASTROFLEET_CONFIG", "")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "fleet.json"), []byte(`{"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveConfigPath(""); err != nil || got != "fleet.json" {
		t.Fatalf("cwd search: got %q, %v; want fleet.json", got, err)
	}
}

func TestResolveConfigPathNotFound(t *testing.T) {
	// A deployed box may actually have /etc/astrofleet/fleet.json, which would legitimately
	// be found — skip the not-found assertion there.
	if _, err := os.Stat("/etc/astrofleet/fleet.json"); err == nil {
		t.Skip("/etc/astrofleet/fleet.json exists on this host")
	}
	t.Setenv("ASTROFLEET_CONFIG", "")
	t.Chdir(t.TempDir()) // empty dir → no ./fleet.json
	_, err := resolveConfigPath("")
	if err == nil {
		t.Fatal("want an error when no config file exists anywhere")
	}
	if !strings.Contains(err.Error(), "/etc/astrofleet/fleet.json") {
		t.Errorf("error should list the searched paths, got: %v", err)
	}
}
