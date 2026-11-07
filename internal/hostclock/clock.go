package hostclock

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var ErrUndisciplined = errors.New("neither chronyd nor ntpd is running")

// ReadClock queries only the running system time client. A failed query is not
// evidence that the daemon is absent, and never causes a switch to another client.
func ReadClock(ctx context.Context) (Clock, error) {
	return readClock(ctx, activeTimeClient, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	})
}

func readClock(ctx context.Context, detect func(context.Context) (string, error), run func(context.Context, string, ...string) ([]byte, error)) (Clock, error) {
	client, err := detect(ctx)
	if err != nil {
		return Clock{}, err
	}
	c := Clock{Client: client}
	var data []byte
	switch client {
	case "Chrony":
		data, err = run(ctx, "chronyc", "-n", "-c", "tracking")
		if err == nil {
			q, err := parseTracking(string(data))
			q.Client = client
			return q, err
		}
	case "ntpd":
		data, err = run(ctx, "ntpq", "-n", "-c", "rv 0 leap,stratum,refid,offset,rootdelay,rootdisp", "127.0.0.1")
		if err == nil {
			q, err := parseNTPQ(string(data))
			q.Client = client
			return q, err
		}
	default:
		return c, ErrUndisciplined
	}
	return c, fmt.Errorf("%s statistics: %w", client, err)
}

func activeTimeClient(ctx context.Context) (string, error) {
	var names []string
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return "", err
		}
		var denied bool
		for _, e := range entries {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if _, err := strconv.Atoi(e.Name()); err != nil {
				continue
			}
			b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
			if errors.Is(err, os.ErrPermission) {
				denied = true
			}
			if err == nil {
				names = append(names, strings.TrimSpace(string(b)))
			}
		}
		client, err := selectTimeClient(names)
		if client == "" && err == nil && denied {
			return "", fmt.Errorf("cannot inspect all time-client processes")
		}
		return client, err
	}
	if runtime.GOOS == "windows" {
		b, err := exec.CommandContext(ctx, "tasklist.exe", "/FO", "CSV", "/NH").Output()
		if err != nil {
			return "", fmt.Errorf("detect time client: %w", err)
		}
		client, err := timeClientFromTasklist(string(b))
		return nonLinuxTimeClient(client, err)
	}
	b, err := exec.CommandContext(ctx, "ps", "-A", "-o", "comm=").Output()
	if err != nil {
		return "", fmt.Errorf("detect time client: %w", err)
	}
	client, err := selectTimeClient(strings.Split(string(b), "\n"))
	return nonLinuxTimeClient(client, err)
}

func selectTimeClient(names []string) (string, error) {
	chrony, ntp := false, false
	for _, name := range names {
		switch strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(name))), ".exe") {
		case "chronyd":
			chrony = true
		case "ntpd":
			ntp = true
		}
	}
	if chrony && ntp {
		return "", fmt.Errorf("both chronyd and ntpd are running; clock discipline is ambiguous")
	}
	if chrony {
		return "Chrony", nil
	}
	if ntp {
		return "ntpd", nil
	}
	return "", nil
}

// tasklist CSV quotes image names and fields containing commas.
func timeClientFromTasklist(s string) (string, error) {
	reader := csv.NewReader(strings.NewReader(s))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return "", fmt.Errorf("read process list: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) > 0 {
			names = append(names, row[0])
		}
	}
	return selectTimeClient(names)
}

// Native OS time services may discipline the clock without chronyd or ntpd.
// Their absence on these platforms therefore does not prove an undisciplined clock.
func nonLinuxTimeClient(client string, err error) (string, error) {
	if client == "" && err == nil {
		return "", fmt.Errorf("no supported time client detected; native OS time-service statistics unavailable")
	}
	return client, err
}
