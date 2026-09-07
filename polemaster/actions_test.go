package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

func TestPixelScaleHTTP(t *testing.T) {
	p := newTestDriver(t, newFake())
	defer p.Close(context.Background())
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
	})
	if err := srv.Register(alpacadev.CameraType, 0, p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		member, form, want string
		code               int
	}{
		{"supportedactions", "", `["PixelScale"]`, 0},
		{"action", "Action=PixelScale&Parameters=", `"31.0276"`, 0},
		{"action", "Action=pIxElScAlE&Parameters=", `"31.0276"`, 0},
		{"action", "Action=PixelScale&Parameters=20", ``, 1025},
		{"action", "Action=unknown&Parameters=", ``, 1036},
		{"pixelsizex", "", `3.75`, 0},
		{"pixelsizey", "", `3.75`, 0},
	} {
		t.Run(tc.member+tc.form, func(t *testing.T) {
			method := http.MethodGet
			if tc.form != "" {
				method = http.MethodPut
			}
			r := httptest.NewRequest(method, "/api/v1/camera/0/"+tc.member+"?ClientID=1&ClientTransactionID=1", strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			var got struct {
				Value       json.RawMessage
				ErrorNumber int
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || got.ErrorNumber != tc.code || (tc.want != "" && string(got.Value) != tc.want) {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
