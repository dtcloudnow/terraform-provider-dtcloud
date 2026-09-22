package router_test

import (
	"os"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/wait"
)

// TestMain shortens the waiters for the offline suite. The fake API settles in
// a known number of reads, so the production pacing only adds wall-clock time.
//
// An acceptance run against the real API keeps the production pacing.
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC") == "" {
		wait.ShortenForTests()
	}
	os.Exit(m.Run())
}
