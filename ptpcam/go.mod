module github.com/mikefsq/goalpaca-devices/ptpcam

go 1.25.3

require (
	github.com/mikefsq/goalpaca v0.3.1
	github.com/mikefsq/ptp v0.0.0-20260808005538-12ef290080bf
)

// TEMPORARY. github.com/mikefsq/ptp is a new repository that `go` cannot fetch
// yet: it resolves over HTTPS and gets an auth prompt, because the repo is
// private and GOPRIVATE is unset. Every other sibling here is reached through
// go.work alone.
//
// Remove this once ptp is fetchable — either by publishing it, or by setting
//
//	go env -w GOPRIVATE=github.com/mikefsq/*
//
// which is the better fix, since it covers every private module rather than
// this one, and lets the pseudo-version above do its job.
replace github.com/mikefsq/ptp => ../../ptp
