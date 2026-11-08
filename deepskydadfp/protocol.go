package driver

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go.bug.st/serial"
)

const maxBrightness = 4096
const commandTimeout = 2 * time.Second

// link is exclusively owned by Panel; its mutex serializes whole transactions.
// A failed or ambiguous transaction invalidates the link; writes are never replayed.
type link interface {
	io.ReadWriteCloser
}

type rejected struct{ command, reply string }

func (e rejected) Error() string { return fmt.Sprintf("%s rejected: %s", e.command, e.reply) }

func exchange(p link, command string) (string, error) {
	data := []byte("[" + command + "]")
	n, err := p.Write(data)
	if err != nil {
		return "", err
	}
	if n != len(data) {
		return "", io.ErrShortWrite
	}
	deadline := time.Now().Add(commandTimeout)
	reply := make([]byte, 0, 128)
	started := false
	for time.Now().Before(deadline) {
		var b [1]byte
		n, err := p.Read(b[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		if !started {
			if b[0] == '(' {
				started = true
			}
			continue
		}
		if b[0] == ')' {
			return string(reply), nil
		}
		if b[0] < 32 || b[0] > 126 || len(reply) >= 255 {
			return "", fmt.Errorf("malformed response to %s", command)
		}
		reply = append(reply, b[0])
	}
	return "", fmt.Errorf("response to %s timed out", command)
}

func acknowledge(p link, command string) error {
	reply, err := exchange(p, command)
	if err != nil {
		return err
	}
	if reply != "OK" {
		return rejected{command, reply}
	}
	return nil
}

func number(p link, command string, min, max int) (int, error) {
	text, err := exchange(p, command)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("invalid %s response %q", command, text)
	}
	return n, nil
}

func openSerial(path string) (link, error) {
	p, err := serial.Open(path, &serial.Mode{BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
	if err != nil {
		return nil, err
	}
	if err = p.SetReadTimeout(100 * time.Millisecond); err == nil {
		err = p.ResetInputBuffer()
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func identify(p link) (string, int, error) {
	firmware, err := exchange(p, "GFRM")
	if err != nil {
		return "", 0, err
	}
	if !strings.Contains(firmware, "DeepSkyDad.FP2") {
		return "", 0, fmt.Errorf("not an FP2-family panel: %q", firmware)
	}
	product, err := number(p, "GPRD", 1, 5)
	return firmware, product, err
}

func isRejected(err error) bool {
	var r rejected
	return errors.As(err, &r)
}
