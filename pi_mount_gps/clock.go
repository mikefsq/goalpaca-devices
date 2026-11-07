package driver

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// chronySynced reports whether chrony has the system clock synchronised, from
// the leap-status field of `chronyc -c tracking`. It returns false when chrony
// is not installed or not running, or has no source yet (leap status "Not
// synchronised"), so the feed falls back to GPS time.
func chronySynced() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "chronyc", "-c", "tracking").Output()
	if err != nil {
		return false
	}
	return parseChronyTracking(string(out))
}

// parseChronyTracking reads the CSV form of `chronyc tracking`, whose last field
// is the leap status.
func parseChronyTracking(csv string) bool {
	fields := strings.Split(strings.TrimSpace(csv), ",")
	if len(fields) < 14 {
		return false
	}
	return fields[len(fields)-1] == "Normal"
}
