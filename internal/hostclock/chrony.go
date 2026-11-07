// Package hostclock reads the accuracy reported by the host time client.
package hostclock

import (
	"encoding/csv"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Clock struct {
	Client    string  `json:"client"`
	Stratum   int     `json:"stratum"`
	Reference string  `json:"reference"`
	ErrorMs   float64 `json:"error_ms"`
	Synced    bool    `json:"synced"`
}

func parseTracking(s string) (Clock, error) {
	r, err := csv.NewReader(strings.NewReader(s)).Read()
	if err != nil || len(r) != 14 {
		return Clock{}, fmt.Errorf("invalid Chrony tracking report")
	}
	stratum, err := strconv.Atoi(r[2])
	if err != nil {
		return Clock{}, err
	}
	values := make([]float64, 3)
	for i, col := range []int{4, 10, 11} {
		v, e := strconv.ParseFloat(r[col], 64)
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return Clock{}, fmt.Errorf("invalid Chrony error estimate")
		}
		values[i] = v
	}
	if values[2] < 0 {
		return Clock{}, fmt.Errorf("negative Chrony dispersion")
	}
	c := Clock{Client: "Chrony", Stratum: stratum, Reference: r[0], ErrorMs: 1000 * (math.Abs(values[0]) + math.Abs(values[1])/2 + values[2])}
	if r[1] != "" {
		c.Reference = r[1]
	}
	if math.IsInf(c.ErrorMs, 0) {
		return Clock{}, fmt.Errorf("Chrony error estimate overflow")
	}
	c.Synced = stratum > 0 && stratum < 16 && r[0] != "7F7F0101" && (r[13] == "Normal" || r[13] == "Insert second" || r[13] == "Delete second")
	return c, nil
}
