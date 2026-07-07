module github.com/mikefsq/goalpaca-devices/fleet

go 1.25.0

// The driver modules below are subdirectories of this same repo, cross-referenced
// before this module-path rename has been committed and pushed — so no real,
// resolvable version can exist for them yet (go.work's workspace override alone
// doesn't suppress Go's version-validation network call for a pinned version that
// doesn't correspond to any real commit). Once the rename lands upstream, these
// could switch to a real pseudo-version and drop the replace, matching goalpaca/
// astrocam/goindi/lx200 below. Until then, replace is the correct, ordinary tool
// for same-repo intra-module cross-references.
replace (
	github.com/mikefsq/goalpaca-devices/asiam5 => ../asiam5
	github.com/mikefsq/goalpaca-devices/astrocam => ../astrocam
	github.com/mikefsq/goalpaca-devices/asieaf => ../asieaf
	github.com/mikefsq/goalpaca-devices/asiefw => ../asiefw
	github.com/mikefsq/goalpaca-devices/focuscube => ../focuscube
	github.com/mikefsq/goalpaca-devices/focuslynx => ../focuslynx
	github.com/mikefsq/goalpaca-devices/oasisfoc => ../oasisfoc
	github.com/mikefsq/goalpaca-devices/oasisfw => ../oasisfw
	github.com/mikefsq/goalpaca-devices/onstep => ../onstep
	github.com/mikefsq/goalpaca-devices/rst => ../rst
	github.com/mikefsq/goalpaca-devices/tenmicron => ../tenmicron
)

require (
	github.com/mikefsq/astrocam v0.0.0-20260704045614-8b60fa35c2d6
	github.com/mikefsq/goalpaca v0.2.0
	github.com/mikefsq/goalpaca-devices/asiam5 v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/astrocam v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/asieaf v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/asiefw v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/focuscube v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/focuslynx v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/oasisfoc v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/oasisfw v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/onstep v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/rst v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goalpaca-devices/tenmicron v0.0.0-00010101000000-000000000000
	github.com/mikefsq/goindi v0.0.0-20260623000347-2dda0b2dec05
	github.com/mikefsq/lx200 v0.1.0
	golang.org/x/net v0.46.0
)

require (
	github.com/mikefsq/goasi v0.1.1-0.20260603174043-6a254265cec6 // indirect
	github.com/mikefsq/oasis-astro v0.0.0-20260613070221-c6e70b94291f // indirect
	github.com/mikefsq/optec v0.0.0-20260707021816-df3786ba6eb4 // indirect
	github.com/mikefsq/pegasus-astro v0.0.0-20260610070031-afd43eb66e1d // indirect
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
