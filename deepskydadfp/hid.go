package driver

// The panel's optional HID interface, in firmware built with FP2_USB_HID. hidLink translates
// the serial command text this driver already uses into HID reports, so the panel logic is the
// same over both transports. The report layouts must match src/panel_hid.h in the deepskydad
// firmware project.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	hidVID = 0x2E8A
	hidPID = 0x000A

	hidReportCommand = 1
	hidReportStatus  = 2
	hidReportInfo    = 3
	hidCommandBytes  = 12
	hidStatusBytes   = 42
	hidInfoBytes     = 34
	hidVersionBytes  = 32
	hidProtocol      = 1

	hidOpSetLight      = 0x10
	hidOpSetBrightness = 0x11
	hidOpClearFault    = 0x12
	hidOpSetMode       = 0x13
	hidOpSetPWMHz      = 0x14
	hidOpSetDivider    = 0x15
	hidOpStop          = 0x22
	hidOpMove          = 0x23
	hidOpSetHeaterMode = 0x30
	hidOpSetHeaterPwr  = 0x31

	hidStateDone      = 2
	hidPollDelay      = 2 * time.Millisecond
	hidCoverMoving    = 2
	hidNoTemperature  = -32768
	hidNoTimeout      = 0xffff
	hidModePWM        = 0
	hidModeDeltaSigma = 6

	// Below the range the driver accepts, so a missing reading reports an unavailable sensor.
	hidUnavailableTemperature = "-999"
	hidUnknownCommand         = "!100"
)

// errNoSingleHIDPanel is the normal result for firmware without HID, so it isn't logged.
var errNoSingleHIDPanel = errors.New("no single panel HID interface found")

// hidLink is a link whose Write performs the whole transaction and buffers the framed reply for
// Read. A failed or timed-out command returns an error, so the panel drops the link and never
// replays a write.
type hidLink struct {
	t        transport
	reply    bytes.Buffer
	sequence byte
	target   int
}

func (h *hidLink) Close() error { return h.t.close() }

func (h *hidLink) Read(b []byte) (int, error) { return h.reply.Read(b) }

func (h *hidLink) Write(b []byte) (int, error) {
	command := strings.TrimSuffix(strings.TrimPrefix(string(b), "["), "]")
	reply, err := h.run(command)
	if err != nil {
		return 0, err
	}
	h.reply.WriteString("(" + reply + ")")
	return len(b), nil
}

// run answers one command with the text serial firmware would send inside its parentheses.
func (h *hidLink) run(command string) (string, error) {
	if len(command) < 4 {
		return hidUnknownCommand, nil
	}
	key, argument := command[:4], command[4:]
	value, numeric := 0, false
	if parsed, err := strconv.Atoi(argument); err == nil && parsed >= 0 {
		value, numeric = parsed, true
	}
	switch key {
	case "GFRM":
		info, err := h.t.getFeature(hidReportInfo, hidInfoBytes)
		if err != nil {
			return "", err
		}
		version, _, _ := strings.Cut(string(info[:hidVersionBytes]), "\x00")
		return "Board=DeepSkyDad.FP2, Version=" + version, nil
	case "GPRD":
		info, err := h.t.getFeature(hidReportInfo, hidInfoBytes)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(info[hidVersionBytes])), nil
	case "GMOV", "GOPS", "GLON", "GLBR", "GHTM", "GHTT":
		status, err := h.t.getFeature(hidReportStatus, hidStatusBytes)
		if err != nil {
			return "", err
		}
		return statusField(key, status), nil
	case "STRG":
		// HID has one command that sets the target and starts the move, so it waits for SMOV.
		if !numeric {
			return "!101", nil
		}
		h.target = value
		return "OK", nil
	case "SMOV":
		return h.send(hidOpMove, uint32(h.target))
	case "STOP":
		return h.send(hidOpStop, 0)
	case "SLBR", "SLON", "SHTM":
		if !numeric {
			return "!101", nil
		}
		opcode := map[string]byte{"SLBR": hidOpSetBrightness, "SLON": hidOpSetLight, "SHTM": hidOpSetHeaterMode}[key]
		return h.send(opcode, uint32(value))
	}
	return hidUnknownCommand, nil
}

// statusField reads one value from the status report, in the field order of hid_status_t.
func statusField(key string, s []byte) string {
	le := binary.LittleEndian
	switch key {
	case "GMOV":
		if s[4] == hidCoverMoving {
			return "1"
		}
		return "0"
	case "GOPS":
		return strconv.Itoa(int(s[4]))
	case "GLON":
		return strconv.Itoa(int(s[8]))
	case "GLBR":
		return strconv.Itoa(int(le.Uint16(s[10:])))
	case "GHTM":
		return strconv.Itoa(int(s[14]))
	}
	centi := int16(le.Uint16(s[18:]))
	if centi == hidNoTemperature {
		return hidUnavailableTemperature
	}
	return strconv.FormatFloat(float64(centi)/100, 'f', 2, 64)
}

// hidStatus is the JSON form of the status report, for the Status action.
type hidStatus struct {
	Position         int      `json:"position"`
	Target           int      `json:"target"`
	Cover            string   `json:"cover"`
	Calibrated       bool     `json:"calibrated"`
	PositionKnown    bool     `json:"positionKnown"`
	MotorFault       int      `json:"motorFault"`
	LightOn          bool     `json:"lightOn"`
	LightOffReason   int      `json:"lightOffReason"`
	Brightness       int      `json:"brightness"`
	MinutesToAutoOff *int     `json:"minutesToAutoOff"`
	HeaterMode       int      `json:"heaterMode"`
	HeaterPower      int      `json:"heaterPower"`
	TemperatureC     *float64 `json:"temperatureC"`
	OutputMode       string   `json:"outputMode"`
	Divider          int      `json:"divider"`
	PWMRequestedHz   uint32   `json:"pwmRequestedHz"`
	PWMActualHz      float64  `json:"pwmActualHz"`
	Streaming        bool     `json:"streaming"`
	OutputOK         bool     `json:"outputOk"`
	StreamFault      bool     `json:"streamFault"`
	Underruns        uint32   `json:"underruns"`
	TXStalls         uint32   `json:"txStalls"`
}

// decodeStatus reads the status report, in the field order of hid_status_t.
func decodeStatus(s []byte) hidStatus {
	le := binary.LittleEndian
	status := hidStatus{
		Position: int(le.Uint16(s[0:])), Target: int(le.Uint16(s[2:])),
		Cover:      map[byte]string{0: "closed", 1: "open", 2: "moving"}[s[4]],
		Calibrated: s[5] != 0, PositionKnown: s[6] != 0, MotorFault: int(s[7]),
		LightOn: s[8] != 0, LightOffReason: int(s[9]), Brightness: int(le.Uint16(s[10:])),
		HeaterMode: int(s[14]), HeaterPower: int(le.Uint16(s[16:])),
		OutputMode: "pwm", Divider: int(le.Uint16(s[24:])),
		PWMRequestedHz: le.Uint32(s[26:]), PWMActualHz: float64(le.Uint32(s[30:])) / 1000,
		OutputOK: s[21] != 0, StreamFault: s[22] != 0, Streaming: s[23] != 0,
		Underruns: le.Uint32(s[34:]), TXStalls: le.Uint32(s[38:]),
	}
	if s[20] == hidModeDeltaSigma {
		status.OutputMode = "delta-sigma"
	}
	if minutes := le.Uint16(s[12:]); minutes != hidNoTimeout {
		value := int(minutes)
		status.MinutesToAutoOff = &value
	}
	if centi := int16(le.Uint16(s[18:])); centi != hidNoTemperature {
		value := float64(centi) / 100
		status.TemperatureC = &value
	}
	return status
}

// send writes a command and waits for its sequence number to show it finished.
func (h *hidLink) send(opcode byte, value uint32) (string, error) {
	h.sequence++
	request := make([]byte, hidCommandBytes)
	request[0], request[1] = opcode, h.sequence
	binary.LittleEndian.PutUint32(request[4:], value)
	if err := h.t.setFeature(hidReportCommand, request); err != nil {
		return "", err
	}
	deadline := time.Now().Add(commandTimeout)
	for time.Now().Before(deadline) {
		reply, err := h.t.getFeature(hidReportCommand, hidCommandBytes)
		if err != nil {
			return "", err
		}
		if reply[1] == h.sequence && reply[2] == hidStateDone {
			code := binary.LittleEndian.Uint16(reply[8:])
			if code == 0 {
				return "OK", nil
			}
			return fmt.Sprintf("!%d", code), nil
		}
		time.Sleep(hidPollDelay)
	}
	return "", fmt.Errorf("HID command 0x%02x did not finish within %s", opcode, commandTimeout)
}

// openHIDPanel opens the panel's HID interface. It returns an error if the panel has none, so the
// caller falls back to serial. Other Pico devices share this VID and PID, so the product name and
// protocol version are both checked.
func openHIDPanel(cfg Config) (link, error) {
	devices, err := listHID(hidVID, hidPID)
	if err != nil {
		return nil, err
	}
	var matches []hidDev
	for _, device := range devices {
		if !strings.Contains(strings.ToLower(device.product), "deep sky dad fp2") {
			continue
		}
		if cfg.Serial == "" || device.serial == cfg.Serial {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return nil, errNoSingleHIDPanel
	}
	t, err := openHID(matches[0].path)
	if err != nil {
		return nil, err
	}
	info, err := t.getFeature(hidReportInfo, hidInfoBytes)
	if err != nil {
		t.close()
		return nil, err
	}
	if info[hidVersionBytes+1] != hidProtocol {
		t.close()
		return nil, fmt.Errorf("unsupported HID protocol %d", info[hidVersionBytes+1])
	}
	return &hidLink{t: t}, nil
}

// openPanel uses HID when it is available, and serial otherwise. An explicit path always means
// serial.
func openPanel(cfg Config) (link, error) {
	if cfg.Path == "" {
		port, err := openHIDPanel(cfg)
		if err == nil {
			return port, nil
		}
		if !errors.Is(err, errNoSingleHIDPanel) {
			log.Printf("deepskydadfp: HID unavailable, using serial: %v", err)
		}
	}
	path, err := resolvePath(cfg)
	if err != nil {
		return nil, err
	}
	return openSerial(path)
}
