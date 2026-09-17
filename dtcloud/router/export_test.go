package router

import "time"

// SetWaitPacingForTests drops the waiters' poll pacing for the offline suite.
// Exported through this file so the external test package can reach it without
// the production code carrying a test-only switch.
func SetWaitPacingForTests() {
	waitDelay = 10 * time.Millisecond
	waitMinTimeout = 10 * time.Millisecond
}
