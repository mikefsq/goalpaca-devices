package driver

import (
	"bytes"
	"log"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// A stopped stream must fail each pending exposure while logging an identical
// failure only once. A later frame completes the operation and ends the streak.
func TestVideoFailureDiagnosticsAndRecovery(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	c := NewPureASICamera(0, "")
	c.SetName("Guide camera")
	wait := func() {
		c.exposeOp.Begin()
		c.exposeWG.Add(1)
		c.waitVideoFrame(1, 0)
	}
	for i := 0; i < 3; i++ {
		wait()
		if err := c.exposeOp.Err(); err == nil || !strings.Contains(err.Error(), "video mode stopped") {
			t.Fatalf("exposure %d lost its failure: %v", i, err)
		}
	}
	if got := strings.Count(output.String(), "exposure failed:"); got != 1 {
		t.Fatalf("got %d failure logs: %s", got, output.String())
	}
	c.videoOn, c.vidSeq = true, 1
	c.vidFrame, c.vidW, c.vidH, c.vidBpp = []byte{1, 2}, 1, 1, 2
	wait()
	if c.exposeOp.Err() != nil || c.exposeOp.State() != alpacadev.OpDone {
		t.Fatalf("recovery did not complete: %v", c.exposeOp.Err())
	}
	if !bytes.Equal(c.frame, []byte{1, 2}) {
		t.Fatal("recovery lost the frame")
	}
	if !strings.Contains(output.String(), "exposure succeeded after 2 repeat(s)") {
		t.Fatal(output.String())
	}
	c.videoOn = false
	wait()
	if got := strings.Count(output.String(), "exposure failed:"); got != 2 {
		t.Fatalf("new failure after recovery wasn't logged: %s", output.String())
	}
}
