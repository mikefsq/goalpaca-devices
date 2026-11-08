package driver

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

const (
	minPWMHz   = 1000
	maxPWMHz   = 100000
	minDivider = 4
	maxDivider = 1280
	maxHeater  = 256
	maxAngle   = 270
)

// Actions follow the fleet convention: empty parameters read a setting, and a value writes it.
// Read-only actions and operations reject a value. Names are case-insensitive. The HID actions
// only exist while the panel is connected over HID.
func (p *Panel) SupportedActions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	actions := []string{}
	heater := p.product != 2 && p.product != 5
	if heater {
		actions = append(actions, "HeaterTemperature", "HeaterMode")
	}
	if _, isHID := p.port.(*hidLink); isHID {
		actions = append(actions, "Status")
		// SFP2 has no light, so it has no output to configure.
		if p.product != 5 {
			actions = append(actions, "ClearFault", "OutputMode", "PWMFrequency", "DeltaSigmaDivider")
		}
		if heater {
			actions = append(actions, "HeaterPower")
		}
		if p.product != 2 {
			actions = append(actions, "CoverPosition")
		}
	}
	return actions
}

func isHIDAction(name string) bool {
	switch name {
	case "status", "clearfault", "outputmode", "pwmfrequency", "deltasigmadivider", "heaterpower", "coverposition":
		return true
	}
	return false
}

func (p *Panel) Action(name, parameters string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	parameters = strings.TrimSpace(parameters)
	if isHIDAction(name) {
		return p.hidAction(name, parameters)
	}
	if name != "heatertemperature" && name != "heatermode" {
		return "", alpacadev.ErrActionNotImplemented
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return "", alpacadev.ErrNotConnected
	}
	if p.product == 2 || p.product == 5 {
		return "", alpacadev.ErrNotImplemented
	}
	if name == "heatermode" {
		if parameters == "" {
			return p.heaterModeLocked()
		}
		mode, err := strconv.Atoi(parameters)
		if err != nil || mode < 0 || mode > 2 {
			return "", alpacadev.ErrInvalidValue
		}
		if err := p.ackLocked(fmt.Sprintf("SHTM%d", mode)); err != nil {
			return "", err
		}
		actual, err := p.heaterModeLocked()
		if err != nil {
			return "", err
		}
		if actual != strconv.Itoa(mode) {
			return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, "heater mode readback did not match requested mode")
		}
		return actual, nil
	}
	if parameters != "" {
		return "", alpacadev.ErrInvalidValue
	}
	reply, err := p.heaterQueryLocked("GHTT")
	if err != nil {
		return "", err
	}
	temperature, err := strconv.ParseFloat(reply, 64)
	if err != nil || math.IsNaN(temperature) || math.IsInf(temperature, 0) {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, fmt.Sprintf("invalid heater temperature %q", reply))
	}
	// Vendor UI treats this range as a disconnected/missing heater sensor.
	if temperature < -40 || temperature > 200 {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, "heater temperature sensor is unavailable")
	}
	return strconv.FormatFloat(temperature, 'f', -1, 64), nil
}

func (p *Panel) heaterModeLocked() (string, error) {
	reply, err := p.heaterQueryLocked("GHTM")
	if err != nil {
		return "", err
	}
	mode, err := strconv.Atoi(reply)
	if err != nil || mode < 0 || mode > 2 {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, fmt.Sprintf("invalid heater mode %q", reply))
	}
	return strconv.Itoa(mode), nil
}

func (p *Panel) heaterQueryLocked(command string) (string, error) {
	reply, err := exchange(p.port, command)
	if err != nil {
		p.dropLocked()
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, err.Error())
	}
	if reply == "!100" {
		return "", alpacadev.ErrNotImplemented
	}
	if strings.HasPrefix(reply, "!") {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, fmt.Sprintf("%s rejected: %s", command, reply))
	}
	return reply, nil
}

// hidAction runs an action that needs the HID interface. Over serial it reports the action as not
// implemented, the same as an unknown name. Values are checked here, and the firmware checks them
// again.
func (p *Panel) hidAction(name, parameters string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return "", alpacadev.ErrNotConnected
	}
	h, isHID := p.port.(*hidLink)
	if !isHID {
		return "", alpacadev.ErrActionNotImplemented
	}
	switch name {
	case "clearfault", "outputmode", "pwmfrequency", "deltasigmadivider":
		if p.product == 5 {
			return "", alpacadev.ErrNotImplemented
		}
	case "heaterpower":
		if p.product == 2 || p.product == 5 {
			return "", alpacadev.ErrNotImplemented
		}
	case "coverposition":
		if p.product == 2 {
			return "", alpacadev.ErrNotImplemented
		}
	}
	switch name {
	case "status":
		if parameters != "" {
			return "", alpacadev.ErrInvalidValue
		}
		return p.hidStatusLocked(h)
	case "clearfault":
		if parameters != "" {
			return "", alpacadev.ErrInvalidValue
		}
		return p.hidSendLocked(h, hidOpClearFault, 0, "OK")
	case "outputmode":
		switch parameters {
		case "":
			status, err := p.hidReadStatusLocked(h)
			return status.OutputMode, err
		case "pwm":
			return p.hidSendLocked(h, hidOpSetMode, hidModePWM, parameters)
		case "delta-sigma":
			return p.hidSendLocked(h, hidOpSetMode, hidModeDeltaSigma, parameters)
		}
		return "", alpacadev.ErrInvalidValue
	}
	if parameters == "" {
		status, err := p.hidReadStatusLocked(h)
		if err != nil {
			return "", err
		}
		switch name {
		case "pwmfrequency":
			return strconv.FormatUint(uint64(status.PWMRequestedHz), 10), nil
		case "deltasigmadivider":
			return strconv.Itoa(status.Divider), nil
		case "heaterpower":
			return strconv.Itoa(status.HeaterPower), nil
		}
		return strconv.Itoa(status.Position), nil
	}
	value, err := strconv.Atoi(parameters)
	if err != nil {
		return "", alpacadev.ErrInvalidValue
	}
	switch name {
	case "pwmfrequency":
		if value < minPWMHz || value > maxPWMHz {
			return "", alpacadev.ErrInvalidValue
		}
		return p.hidSendLocked(h, hidOpSetPWMHz, uint32(value), parameters)
	case "deltasigmadivider":
		if value < minDivider || value > maxDivider {
			return "", alpacadev.ErrInvalidValue
		}
		return p.hidSendLocked(h, hidOpSetDivider, uint32(value), parameters)
	case "heaterpower":
		if value < 0 || value > maxHeater {
			return "", alpacadev.ErrInvalidValue
		}
		return p.hidSendLocked(h, hidOpSetHeaterPwr, uint32(value), parameters)
	}
	// A position goes through the same checks and move tracking as OpenCover and CloseCover.
	if value < 0 || value > maxAngle {
		return "", alpacadev.ErrInvalidValue
	}
	if p.light != nil && p.light.waitingForCover {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "cover is closing for illumination; turn the calibrator off or halt first")
	}
	if err := p.moveLocked(value); err != nil {
		return "", err
	}
	return parameters, nil
}

func (p *Panel) hidReadStatusLocked(h *hidLink) (hidStatus, error) {
	raw, err := h.t.getFeature(hidReportStatus, hidStatusBytes)
	if err != nil {
		p.dropLocked()
		return hidStatus{}, alpacadev.NewError(alpacadev.ErrNumDriverBase, err.Error())
	}
	return decodeStatus(raw), nil
}

func (p *Panel) hidStatusLocked(h *hidLink) (string, error) {
	status, err := p.hidReadStatusLocked(h)
	if err != nil {
		return "", err
	}
	text, err := json.Marshal(status)
	if err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, err.Error())
	}
	return string(text), nil
}

// hidSendLocked treats a transport failure as an ambiguous write, which drops the link and is
// never replayed. A firmware rejection keeps the link.
func (p *Panel) hidSendLocked(h *hidLink, opcode byte, value uint32, result string) (string, error) {
	reply, err := h.send(opcode, value)
	if err != nil {
		p.dropLocked()
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, err.Error())
	}
	if reply != "OK" {
		return "", alpacadev.NewError(alpacadev.ErrNumDriverBase, fmt.Sprintf("action rejected: %s", reply))
	}
	return result, nil
}
