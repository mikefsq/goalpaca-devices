module github.com/mikefsq/goalpaca-devices

go 1.25.0

require (
	github.com/mikefsq/astrocam v0.1.0
	github.com/mikefsq/astromi.ch v0.1.1
	github.com/mikefsq/goalpaca v0.3.2
	github.com/mikefsq/goasi v0.2.1
	github.com/mikefsq/goindi v0.1.1
	github.com/mikefsq/lx200 v0.2.2
	github.com/mikefsq/oasis-astro v0.1.0
	github.com/mikefsq/optec v0.1.1
	github.com/mikefsq/pegasus-astro v0.1.0
	github.com/mikefsq/pi_mount_gps v0.0.0
	github.com/mikefsq/polemaster v0.1.0
	github.com/mikefsq/ptp v0.1.0
	github.com/mikefsq/scope_hub/driver v0.0.0
	github.com/mikefsq/stellarmate v0.1.0
	github.com/mikefsq/unihedron v0.1.1
)

require (
	github.com/adrianmo/go-nmea v1.10.0 // indirect
	github.com/astrogo/fitsio v0.2.1 // indirect
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// scope_hub is not published yet; build against the local checkout.
replace github.com/mikefsq/scope_hub/driver => ../scope_hub/driver

// Pi GPS/weather hardware support is built against the local checkout.
replace github.com/mikefsq/pi_mount_gps => ../pi_mount_gps

// Timed wire exchanges for TenMicron clock synchronization.
replace github.com/mikefsq/lx200 => ../lx200
