package hostclock

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const ntpdReport = `associd=0 status=0615 leap_none, sync_ntp, 1 event, clock_sync,
leap=00, stratum=2, refid=10.0.1.186,
offset=-0.025, rootdelay=0.100, rootdisp=0.125`

func TestNTPDStatistics(t *testing.T) {
	c, err := parseNTPQ(ntpdReport)
	if err != nil || !c.Synced || c.Client != "ntpd" || c.Stratum != 2 || c.ErrorMs != 0.2 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, s := range []string{strings.Replace(ntpdReport, "leap=00", "leap=11", 1), strings.Replace(ntpdReport, "stratum=2", "stratum=16", 1), strings.Replace(ntpdReport, "10.0.1.186", ".LOCL.", 1)} {
		c, err = parseNTPQ(s)
		if err != nil || c.Synced {
			t.Fatalf("trusted %s: %+v %v", s, c, err)
		}
	}
	for _, s := range []string{"", strings.Replace(ntpdReport, "rootdisp=0.125", "rootdisp=NaN", 1), strings.Replace(ntpdReport, "rootdisp=0.125", "rootdisp=-1", 1), strings.Replace(ntpdReport, "leap=00", "leap=bad", 1)} {
		if _, err := parseNTPQ(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestTimeClientDetection(t *testing.T) {
	for _, tt := range []struct {
		names []string
		want  string
		bad   bool
	}{
		{[]string{"systemd", "chronyd"}, "Chrony", false},
		{[]string{"/usr/sbin/ntpd", "chronyc"}, "ntpd", false},
		{[]string{"chronyc", "ntpq"}, "", false},
		{[]string{"chronyd", "ntpd"}, "", true},
	} {
		got, err := selectTimeClient(tt.names)
		if got != tt.want || (err != nil) != tt.bad {
			t.Fatalf("%v: %q %v", tt.names, got, err)
		}
	}
}

func TestQueriesOnlyActiveTimeClient(t *testing.T) {
	for _, client := range []string{"Chrony", "ntpd", ""} {
		calls := 0
		got, err := readClock(context.Background(), func(context.Context) (string, error) { return client, nil }, func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if client == "Chrony" && name != "chronyc" || client == "ntpd" && name != "ntpq" {
				t.Fatalf("queried wrong client %s", name)
			}
			return nil, errors.New("statistics unavailable")
		})
		if client == "" {
			if calls != 0 || !errors.Is(err, ErrUndisciplined) {
				t.Fatalf("absent daemon: %d %v", calls, err)
			}
		} else if calls != 1 || err == nil || errors.Is(err, ErrUndisciplined) || got.Client != client {
			t.Fatalf("query failure misclassified: %+v %d %v", got, calls, err)
		}
	}
}

func TestWindowsTimeClientList(t *testing.T) {
	client, err := timeClientFromTasklist("\"ntpd.exe\",\"123\",\"Services\",\"0\",\"1,000 K\"\r\n")
	if err != nil || client != "ntpd" {
		t.Fatalf("%q %v", client, err)
	}
	client, err = selectTimeClient([]string{"CHRONYD.EXE"})
	if err != nil || client != "Chrony" {
		t.Fatalf("%q %v", client, err)
	}
}
