package dtcloud

import "testing"

// TestProvider runs the SDK's built-in schema validation over the whole
// provider (resources, data sources, and their attributes). It catches most
// schema wiring mistakes without needing a live API.
func TestProvider(t *testing.T) {
	if err := Provider().InternalValidate(); err != nil {
		t.Fatalf("provider InternalValidate failed: %s", err)
	}
}
