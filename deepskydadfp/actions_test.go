package driver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

func TestHeaterActions(t *testing.T) {
	p, f := setup(t, 1)
	f.override["GHTT"] = "21.375"
	f.override["GHTM"] = "2"
	f.override["SHTM2"] = "OK"
	if got := p.SupportedActions(); !reflect.DeepEqual(got, []string{"HeaterTemperature", "HeaterMode"}) {
		t.Fatal(got)
	}
	for _, test := range []struct{ name, args, want string }{
		{"heaterTEMPERATURE", "", "21.375"},
		{"HeaterMode", "", "2"},
		{"HEATERMODE", "2", "2"},
	} {
		if got, err := p.Action(test.name, test.args); err != nil || got != test.want {
			t.Fatal(test.name, got, err)
		}
	}
	if got := strings.Join(f.commands[len(f.commands)-2:], ","); got != "SHTM2,GHTM" {
		t.Fatal("mode was not verified", got)
	}
	before := len(f.commands)
	for _, args := range []string{"-1", "3", "auto", "1.5", "1][SMOV"} {
		if _, err := p.Action("HeaterMode", args); err != alpacadev.ErrInvalidValue {
			t.Fatal(args, err)
		}
	}
	if _, err := p.Action("HeaterTemperature", "extra"); err != alpacadev.ErrInvalidValue {
		t.Fatal(err)
	}
	if _, err := p.Action("unknown", ""); err != alpacadev.ErrActionNotImplemented {
		t.Fatal(err)
	}
	if len(f.commands) != before {
		t.Fatal("invalid actions touched hardware")
	}
}

func TestHeaterReadbackFailures(t *testing.T) {
	for _, reply := range []string{"NaN", "+Inf", "invalid", "-127", "201", "!101"} {
		p, f := setup(t, 1)
		f.override["GHTT"] = reply
		if _, err := p.Action("HeaterTemperature", ""); err == nil {
			t.Fatal("accepted invalid temperature", reply)
		}
		if !p.Connected() {
			t.Fatal("complete heater response disrupted panel connection", reply)
		}
	}
	p, f := setup(t, 1)
	f.override["SHTM1"], f.override["GHTM"] = "OK", "0"
	before := len(f.commands)
	if _, err := p.Action("HeaterMode", "1"); err == nil {
		t.Fatal("ignored heater command reported success")
	}
	if len(f.commands) != before+2 {
		t.Fatal("heater write replayed")
	}
	f.override["GHTM"] = "!100"
	if _, err := p.Action("HeaterMode", ""); err != alpacadev.ErrNotImplemented {
		t.Fatal(err)
	}
	f.shortWrite = true
	if _, err := p.Action("HeaterMode", "1"); err == nil || !f.closed || p.Connected() {
		t.Fatal("ambiguous write kept connection", err)
	}
}

func TestHeaterCapabilitiesAndConnection(t *testing.T) {
	for _, product := range []int{2, 5} {
		p, f := setup(t, product)
		before := len(f.commands)
		if len(p.SupportedActions()) != 0 {
			t.Fatal("heater advertised on unsupported model")
		}
		if _, err := p.Action("HeaterTemperature", ""); err != alpacadev.ErrNotImplemented || len(f.commands) != before {
			t.Fatal(err)
		}
	}
	p, f := setup(t, 1)
	before := len(f.commands)
	p.MarkDisconnected()
	if _, err := p.Action("HeaterMode", ""); err != alpacadev.ErrNotConnected || len(f.commands) != before {
		t.Fatal(err)
	}
}

func TestHeaterAlpacaActionEndpoint(t *testing.T) {
	p, f := setup(t, 1)
	f.override["GHTT"] = "19.5"
	s := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}})
	if err := s.Register(alpacadev.CoverCalibratorType, 0, p); err != nil {
		t.Fatal(err)
	}
	request := func(method, member string, form url.Values) (any, int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/covercalibrator/0/"+member, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.ServeHTTP(w, r)
		var result struct {
			Value       any
			ErrorNumber int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Value, result.ErrorNumber
	}
	value, code := request(http.MethodGet, "supportedactions", nil)
	if code != 0 || len(value.([]any)) != 2 {
		t.Fatal(value, code)
	}
	value, code = request(http.MethodPut, "action", url.Values{"Action": {"HeaterTemperature"}, "Parameters": {""}})
	if code != 0 || value != "19.5" {
		t.Fatal(value, code)
	}
	if _, code := request(http.MethodPut, "action", url.Values{"Action": {"HeaterMode"}, "Parameters": {"3"}}); code != alpacadev.ErrNumInvalidValue {
		t.Fatal(code)
	}
}
