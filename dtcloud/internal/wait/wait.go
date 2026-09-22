// Package wait carries the poll pacing the resource waiters share.
//
// Every waiter in this provider polls a real endpoint, so the delays between
// reads are tuned for the platform: a few seconds, because a tighter loop only
// adds load without learning anything sooner. The offline suites run against a
// fake API that settles in a known number of reads, where those same delays buy
// nothing and cost wall-clock time -- enough of it that two packages ran past
// Go's default per-package timeout. Pace is the one place that difference is
// expressed, so a waiter keeps its own tuned interval in production.
package wait

import "time"

// testPace is the ceiling the offline suites poll at. It is short enough that
// the waits cost nothing measurable and long enough to stay a real interval.
const testPace = 10 * time.Millisecond

// shortened is set once, before any test runs, and read from the waiters after
// that -- so it needs no synchronisation.
var shortened bool

// Pace returns the interval a waiter should poll at. In production that is the
// duration asked for, unchanged.
func Pace(d time.Duration) time.Duration {
	if shortened && d > testPace {
		return testPace
	}
	return d
}

// ShortenForTests drops the pacing for an offline suite. It belongs in a
// TestMain, guarded on TF_ACC being unset, so that an acceptance run against
// the real API keeps the production intervals.
func ShortenForTests() {
	shortened = true
}
