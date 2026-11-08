package driver

import "go.bug.st/serial/enumerator"

// On macOS the serial library enumerates USB descriptors through IOKit/CoreFoundation
// using cgo. Serial requests themselves use the operating system's serial port.
func enumeratePorts() ([]*portInfo, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	out := make([]*portInfo, 0, len(ports))
	for _, p := range ports {
		out = append(out, &portInfo{Name: p.Name, IsUSB: p.IsUSB, VID: p.VID, PID: p.PID, Product: p.Product, SerialNumber: p.SerialNumber})
	}
	return out, nil
}
