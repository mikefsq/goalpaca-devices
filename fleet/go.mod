module github.com/mikefsq/astrofleet

go 1.25.0

// The fleet is the main module, so it must declare every transitive local
// replace itself — dependency modules' own replace directives are ignored.
replace (
	github.com/mikefsq/asiam5-alpaca => ../asiam5
	github.com/mikefsq/asicam-alpaca => ../astrocam
	github.com/mikefsq/asieaf-alpaca => ../asieaf
	github.com/mikefsq/asiefw-alpaca => ../asiefw
	github.com/mikefsq/astrocam => ../../astrocam
	github.com/mikefsq/focuscube-alpaca => ../focuscube
	github.com/mikefsq/focuslynx-alpaca => ../focuslynx
	github.com/mikefsq/goalpaca => ../../goalpaca
	github.com/mikefsq/goasi => ../../goasi
	github.com/mikefsq/goindi => ../../goindi
	github.com/mikefsq/lx200 => ../../lx200
	github.com/mikefsq/oasis-astro => ../../oasis-astro
	github.com/mikefsq/oasisfoc-alpaca => ../oasisfoc
	github.com/mikefsq/oasisfw-alpaca => ../oasisfw
	github.com/mikefsq/onstep-alpaca => ../onstep
	github.com/mikefsq/optec-astro => ../../optec
	github.com/mikefsq/pegasus-astro => ../../pegasus-astro
	github.com/mikefsq/playeroneAstro => ../../playeroneAstro
	github.com/mikefsq/rst-alpaca => ../rst
	github.com/mikefsq/tenmicron-alpaca => ../tenmicron
)

require (
	github.com/mikefsq/asiam5-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/asicam-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/asieaf-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/asiefw-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/astrocam v0.0.0
	github.com/mikefsq/focuscube-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/focuslynx-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca v0.1.0
	github.com/mikefsq/goindi v0.0.0-00010101000000-000000000000
	github.com/mikefsq/lx200 v0.1.0
	github.com/mikefsq/oasisfoc-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/oasisfw-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/onstep-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/rst-alpaca v0.0.0-00010101000000-000000000000
	github.com/mikefsq/tenmicron-alpaca v0.0.0-00010101000000-000000000000
	golang.org/x/net v0.46.0
)

require (
	github.com/mikefsq/goasi v0.1.1-0.20260603174043-6a254265cec6 // indirect
	github.com/mikefsq/oasis-astro v0.1.0 // indirect
	github.com/mikefsq/optec-astro v0.1.0 // indirect
	github.com/mikefsq/pegasus-astro v0.1.0 // indirect
	github.com/mikefsq/playeroneAstro v0.1.0 // indirect
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
