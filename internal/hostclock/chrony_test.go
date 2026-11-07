package hostclock

import (
	"strings"
	"testing"
)

func TestChronyBound(t *testing.T) {
	s := "50505300,PPS,1,0,0.000001,0,0,0,0,0,0.000020,0.000014,1,Normal"
	q, err := parseTracking(s)
	if err != nil || !q.Synced || q.ErrorMs < 0.024999 || q.ErrorMs > 0.025001 {
		t.Fatalf("%+v %v", q, err)
	}
	q, err = parseTracking(strings.Replace(s, "Normal", "Not synchronised", 1))
	if err != nil || q.Synced {
		t.Fatalf("%+v %v", q, err)
	}
	for _, s := range []string{"", strings.Replace(s, "0.000001", "NaN", 1), strings.Replace(s, "0.000014", "-1", 1)} {
		if _, err := parseTracking(s); err == nil {
			t.Fatal(s)
		}
	}
}
