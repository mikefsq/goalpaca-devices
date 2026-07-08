module github.com/mikefsq/goalpaca-devices/unihedron

go 1.25.0

require (
	github.com/mikefsq/goalpaca v0.2.0
	github.com/mikefsq/unihedron v0.0.0
)

require (
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)

// The unihedron device library is not yet published; build against the local sibling.
// Replace with a pinned pseudo-version once it is pushed (matching the other devices).
replace github.com/mikefsq/unihedron => ../../unihedron
