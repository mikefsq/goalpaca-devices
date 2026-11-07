package hostclock

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ntpq rv reports offset, rootdelay and rootdisp in milliseconds.
func parseNTPQ(s string) (Clock, error) {
	c := Clock{Client: "ntpd"}
	fields := map[string]string{}
	for _, m := range regexp.MustCompile(`([a-z]+)\s*=\s*("[^"]*"|[^,\s]+)`).FindAllStringSubmatch(s, -1) {
		fields[m[1]] = strings.Trim(m[2], `"`)
	}
	var err error
	c.Stratum, err = strconv.Atoi(fields["stratum"])
	if err != nil {
		return c, fmt.Errorf("invalid ntpd stratum")
	}
	leap := fields["leap"]
	if leap != "00" && leap != "01" && leap != "10" && leap != "11" && leap != "0" && leap != "1" && leap != "2" && leap != "3" {
		return c, fmt.Errorf("invalid ntpd leap status")
	}
	c.Reference = fields["refid"]
	if c.Reference == "" {
		return c, fmt.Errorf("missing ntpd reference")
	}
	var values [3]float64
	for i, key := range []string{"offset", "rootdelay", "rootdisp"} {
		v, e := strconv.ParseFloat(fields[key], 64)
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return c, fmt.Errorf("invalid ntpd %s", key)
		}
		values[i] = v
	}
	if values[2] < 0 {
		return c, fmt.Errorf("negative ntpd dispersion")
	}
	c.ErrorMs = math.Abs(values[0]) + math.Abs(values[1])/2 + values[2]
	if math.IsInf(c.ErrorMs, 0) {
		return c, fmt.Errorf("ntpd error estimate overflow")
	}
	c.Synced = c.Stratum > 0 && c.Stratum < 16 && leap != "11" && leap != "3" && strings.Trim(c.Reference, ".") != "LOCL" && c.Reference != "127.127.1.1"
	return c, nil
}
