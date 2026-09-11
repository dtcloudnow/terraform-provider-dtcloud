package sshkey_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// The API emits timestamps without a timezone. Keeping that exact shape here is
// the point of these tests: a bad timestamp used to discard the rest of the
// keypair, taking user_id and deleted with it.
const fakeCreatedAt = acctest.FakeCreatedAt

// fakeUserID mirrors the project id seen in real responses.
const fakeUserID = "82c57cfbb429442989a5695a2a9780f3"

// fakeAPI stands in for the SSH key routes, good enough to drive the provider
// through a full Terraform lifecycle offline.
type fakeAPI struct {
	mu     sync.Mutex
	keys   map[string]string // name -> public key
	nextID int

	// requests records "METHOD /path" for every call that arrived, so tests can
	// assert the provider really round-tripped through the API.
	requests []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{keys: map[string]string{}, nextID: 9307}
}

func (f *fakeAPI) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.keys[name]
	return ok
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	// Every call must carry the API key pair and the region as serverId, so the
	// fake rejects a request without them the way a real one is rejected.
	if r.Header.Get("x-api-access-key") == "" || r.Header.Get("x-api-secret-key") == "" {
		acctest.WriteJSON(w, http.StatusUnauthorized, map[string]any{"errorMessage": "missing api key headers"})
		return
	}
	if r.URL.Query().Get("serverId") == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "serverId is required"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/sshkeys")

	switch {
	case r.Method == http.MethodPost && path == "":
		f.create(w, r)
	case r.Method == http.MethodGet && path == "":
		f.list(w)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/details"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/details")
		f.details(w, name)
	case r.Method == http.MethodDelete && path != "":
		f.delete(w, strings.TrimPrefix(path, "/"))
	default:
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "no such route"})
	}
}

// list answers the way the real endpoint does: name and created, nothing else,
// which is why the plural data source cannot report fingerprints or keys.
func (f *fakeAPI) list(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []map[string]any{}
	for name := range f.keys {
		out = append(out, map[string]any{"name": name, "created": fakeCreatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "bad body"})
		return
	}

	f.mu.Lock()
	f.keys[body.Name] = body.PublicKey
	f.mu.Unlock()

	acctest.WriteJSON(w, http.StatusCreated, struct {
		Keypair createKeypair `json:"keypair"`
	}{createKeypair{
		Name:        body.Name,
		PublicKey:   body.PublicKey,
		Fingerprint: fingerprintFor(body.Name),
		CreatedAt:   fakeCreatedAt,
		UserID:      fakeUserID,
	}})
}

func (f *fakeAPI) details(w http.ResponseWriter, name string) {
	f.mu.Lock()
	pub, ok := f.keys[name]
	id := f.nextID
	f.mu.Unlock()

	if !ok {
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "ssh key not found"})
		return
	}

	acctest.WriteJSON(w, http.StatusOK, struct {
		Keypair detailsKeypair `json:"keypair"`
	}{detailsKeypair{
		Name:        name,
		PublicKey:   pub,
		Fingerprint: fingerprintFor(name),
		CreatedAt:   fakeCreatedAt,
		UserID:      fakeUserID,
		Deleted:     false,
		DeletedAt:   nil,
		ID:          id,
		UpdatedAt:   fakeCreatedAt,
	}})
}

func (f *fakeAPI) delete(w http.ResponseWriter, name string) {
	f.mu.Lock()
	_, ok := f.keys[name]
	delete(f.keys, name)
	f.mu.Unlock()

	if !ok {
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "ssh key not found"})
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// The keypair payloads are structs rather than maps so the field order is the
// API's: a bad timestamp discards everything declared after it, so the ordering
// has to be reproduced for this to be a fair test.

type createKeypair struct {
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
	UserID      string `json:"user_id"`
}

type detailsKeypair struct {
	Name        string  `json:"name"`
	PublicKey   string  `json:"public_key"`
	Fingerprint string  `json:"fingerprint"`
	CreatedAt   string  `json:"created_at"`
	UserID      string  `json:"user_id"`
	Deleted     bool    `json:"deleted"`
	DeletedAt   *string `json:"deleted_at"`
	ID          int     `json:"id"`
	UpdatedAt   string  `json:"updated_at"`
}

func fingerprintFor(name string) string {
	return fmt.Sprintf("c7:f0:0c:%02x", len(name))
}

func testConfig(endpoint, name, publicKey string) string {
	return fmt.Sprintf(`
provider "dtcloud" {
  access_key   = "test-access-key"
  secret_key   = "test-secret-key"
  api_endpoint = %q
  region_id    = "1"
}

resource "dtcloud_ssh_key" "test" {
  name       = %q
  public_key = %q
}

data "dtcloud_ssh_key" "test" {
  name = dtcloud_ssh_key.test.name
}

data "dtcloud_ssh_keys" "all" {
  depends_on = [dtcloud_ssh_key.test]
}
`, endpoint, name, publicKey)
}

const (
	testPublicKey    = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCoriginal terraform-poc"
	testPublicKeyAlt = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCreplaced terraform-poc"
)

// TestAccDtcloudSSHKey_lifecycle drives create → read → ForceNew replace →
// destroy, and asserts created_at and user_id reach state. Those two sit after
// created_at in the response, which is what makes them the regression guard.
func TestAccDtcloudSSHKey_lifecycle(t *testing.T) {
	api := newFakeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const keyName = "tf-acc-ssh-key"

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			if api.has(keyName) {
				return fmt.Errorf("SSH key %q still exists in the API after destroy", keyName)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testConfig(server.URL, keyName, testPublicKey),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The plural data source: the list endpoint reports a name and a
					// timestamp, so anything more has to come from the singular lookup.
					resource.TestCheckResourceAttr("data.dtcloud_ssh_keys.all", "ssh_keys.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_ssh_keys.all", "ssh_keys.0.name", keyName),
					resource.TestCheckResourceAttr("data.dtcloud_ssh_keys.all", "names.0", keyName),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "id", keyName),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "name", keyName),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "public_key", testPublicKey),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "fingerprint", fingerprintFor(keyName)),
					// Parsed from the timezone-less form and re-rendered as RFC 3339.
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "created_at", "2026-07-08T10:35:17Z"),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "user_id", fakeUserID),

					// The data source must see the same key by name.
					resource.TestCheckResourceAttr("data.dtcloud_ssh_key.test", "name", keyName),
					resource.TestCheckResourceAttr("data.dtcloud_ssh_key.test", "fingerprint", fingerprintFor(keyName)),
					resource.TestCheckResourceAttr("data.dtcloud_ssh_key.test", "created_at", "2026-07-08T10:35:17Z"),
					resource.TestCheckResourceAttr("data.dtcloud_ssh_key.test", "user_id", fakeUserID),

					func(*terraform.State) error {
						if !api.has(keyName) {
							return fmt.Errorf("SSH key %q was never created in the API", keyName)
						}
						return nil
					},
				),
			},
			{
				// Re-applying the identical config must be a no-op, so Read round-trips.
				Config:   testConfig(server.URL, keyName, testPublicKey),
				PlanOnly: true,
			},
			{
				// public_key is ForceNew, so changing it replaces the key.
				Config: testConfig(server.URL, keyName, testPublicKeyAlt),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "public_key", testPublicKeyAlt),
					resource.TestCheckResourceAttr("dtcloud_ssh_key.test", "created_at", "2026-07-08T10:35:17Z"),
				),
			},
			{
				// Import by name, the documented workflow.
				ResourceName:      "dtcloud_ssh_key.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudSSHKey_disappears checks the drift path: a key deleted outside
// Terraform is dropped from state rather than erroring, leaving a plan that
// recreates it.
func TestAccDtcloudSSHKey_disappears(t *testing.T) {
	api := newFakeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const keyName = "tf-acc-ssh-key-gone"

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testConfig(server.URL, keyName, testPublicKey),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_ssh_key.test", "id", keyName),
			},
			{
				PreConfig: func() {
					// Delete behind Terraform's back.
					api.mu.Lock()
					delete(api.keys, keyName)
					api.mu.Unlock()
				},
				Config:             testConfig(server.URL, keyName, testPublicKey),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudSSHKey_missingCredentials pins fail-fast in providerConfigure:
// no key pair means a clear error, not a confusing API failure later on.
func TestAccDtcloudSSHKey_missingCredentials(t *testing.T) {
	// The schema falls back to these, so clear them for the duration of the test.
	t.Setenv("DTCLOUD_ACCESS_KEY", "")
	t.Setenv("DTCLOUD_SECRET_KEY", "")

	// And the provider falls back to a configuration file after the environment, so
	// point it at an empty one — otherwise the result depends on whether the machine
	// running the test happens to have credentials configured.
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "dtcloud" {
  region_id   = "1"
  config_file = %q
}

data "dtcloud_ssh_key" "test" {
  name = "anything"
}
`, empty),
				ExpectError: regexp.MustCompile("both .access_key. and .secret_key. must be set"),
			},
		},
	})
}
