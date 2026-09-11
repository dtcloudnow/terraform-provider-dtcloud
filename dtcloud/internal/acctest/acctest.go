// Package acctest holds the shared scaffolding for the provider's acceptance
// tests: the provider factories, the standard provider block and the helpers the
// fake API servers are built from.
//
// It exists to break an import cycle. A test needs dtcloud.Provider(), but
// dtcloud imports every service package, so the service tests live in external
// test packages and reach the provider through here.
package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// FakeCreatedAt is the timestamp the fake API servers report. It carries no
// timezone, exactly as the real API does — the shape that used to break decoding.
const FakeCreatedAt = "2026-07-08T10:35:17.781065"

// FakeCreatedAtRFC3339 is FakeCreatedAt once the provider has parsed and
// re-rendered it, which is what tests assert against.
const FakeCreatedAtRFC3339 = "2026-07-08T10:35:17Z"

// ProviderFactories wires the real provider for use in acceptance tests.
func ProviderFactories() map[string]func() (*schema.Provider, error) {
	return map[string]func() (*schema.Provider, error){
		"dtcloud": func() (*schema.Provider, error) { return dtcloud.Provider(), nil },
	}
}

// ProviderConfig returns a provider block pointed at a fake API server. The
// credentials are dummies; what matters is that they are present.
func ProviderConfig(endpoint string) string {
	return fmt.Sprintf(`
provider "dtcloud" {
  access_key   = "test-access-key"
  secret_key   = "test-secret-key"
  api_endpoint = %q
  region_id    = "1"
}
`, endpoint)
}

// WriteJSON writes a JSON response, as the fake API servers do throughout.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// RequireAuth mirrors the real API's gatekeeping: every request must carry the
// API key pair and a serverId. It reports whether the request may proceed.
func RequireAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("x-api-access-key") == "" || r.Header.Get("x-api-secret-key") == "" {
		WriteJSON(w, http.StatusUnauthorized, map[string]any{"errorMessage": "missing api key headers"})
		return false
	}
	if r.URL.Query().Get("serverId") == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "serverId is required"})
		return false
	}
	return true
}

// NotFound writes the shape the real API uses for a missing resource, structured
// 404 code included — see dterr.IsNotFound.
func NotFound(w http.ResponseWriter, message string) {
	WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": message})
}
