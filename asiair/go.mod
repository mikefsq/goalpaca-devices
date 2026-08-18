module github.com/mikefsq/goalpaca-devices/asiair

go 1.25.0

require (
	github.com/mikefsq/asiair v0.0.0
	github.com/mikefsq/goalpaca v0.3.1
)

require golang.org/x/sys v0.19.0 // indirect

// Both dependencies live in this working tree; drop these when they are tagged
// and published.
replace github.com/mikefsq/goalpaca => ../../goalpaca

replace github.com/mikefsq/asiair => ../../asiair
