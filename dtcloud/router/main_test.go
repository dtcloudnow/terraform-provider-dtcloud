package router_test

import (
	"os"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/router"
)

// TestMain shortens the waiters for the offline suite. The fake API settles in
// a known number of reads, so the production pacing only adds wall-clock time:
// this package makes more waits per step than any other, and at the real delays
// the suite ran for half an hour and came within seconds of the test timeout.
//
// An acceptance run against the real API keeps the production pacing.
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC") == "" {
		router.SetWaitPacingForTests()
	}
	os.Exit(m.Run())
}
