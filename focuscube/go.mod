module github.com/mikefsq/focuscube-alpaca

go 1.25.0

require (
	github.com/mikefsq/goalpaca v0.1.0
	github.com/mikefsq/pegasus-astro v0.1.0
)

require (
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)

replace github.com/mikefsq/pegasus-astro => ../../pegasus-astro

replace github.com/mikefsq/goalpaca => ../../goalpaca
