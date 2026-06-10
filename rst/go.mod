module github.com/mikefsq/rst-alpaca

go 1.25.0

require (
	github.com/mikefsq/goalpaca v0.1.0
	github.com/mikefsq/lx200 v0.1.0
)

require (
	github.com/creack/goselect v0.1.2 // indirect
	go.bug.st/serial v1.6.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)

replace github.com/mikefsq/lx200 => ../../lx200

replace github.com/mikefsq/goalpaca => ../../goalpaca
