package driver

// The HID transport files come from scope_hub/driver.

// transport is a HID feature-report channel to one device.
// hid_darwin.go, hid_linux.go and hid_windows.go provide it.
type transport interface {
	// getFeature reads feature report id and returns its n data bytes,
	// without the report ID.
	getFeature(id byte, n int) ([]byte, error)
	// setFeature writes data as feature report id.
	setFeature(id byte, data []byte) error
	close() error
}

// hidDev is one enumerated HID interface.
type hidDev struct {
	path    string
	serial  string
	product string
}
