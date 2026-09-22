package snapshot_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// checkTerraformOwnedGone asserts everything this provider created is gone.
// Seeded snapshots are skipped by name — Terraform does not own them.
func checkTerraformOwnedGone(api *fakeSnapshotAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, s := range api.snapshots {
			if !strings.HasPrefix(s.Name, "tf-acc-") {
				continue
			}
			if !s.deleted {
				return fmt.Errorf("snapshot %s (%s) still exists after destroy", id, s.Name)
			}
		}
		return nil
	}
}

// snapshotConfig is the whole surface of the package in one configuration.
func snapshotConfig(endpoint, name, volumeID, description string) string {
	desc := ""
	if description != "" {
		desc = fmt.Sprintf("  description = %q\n", description)
	}
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name      = %q
  volume_id = %q
%s}

data "dtcloud_snapshot" "test" {
  id = dtcloud_snapshot.test.id
}

data "dtcloud_snapshots" "all" {
  depends_on = [dtcloud_snapshot.test]
}

data "dtcloud_snapshots" "by_volume" {
  volume_id  = %q
  depends_on = [dtcloud_snapshot.test]
}
`, name, volumeID, desc, volumeID)
}

// bareSnapshotConfig is the resource on its own, for steps that remove the
// snapshot underneath Terraform — the data sources would fail for the wrong
// reason.
func bareSnapshotConfig(endpoint, name, volumeID, description string) string {
	desc := ""
	if description != "" {
		desc = fmt.Sprintf("  description = %q\n", description)
	}
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name      = %q
  volume_id = %q
%s}
`, name, volumeID, desc)
}

// TestAccDtcloudSnapshot_lifecycle drives create → read → re-plan → in-place
// rename → import → destroy. Create is two steps: the endpoint accepts no
// description, so one arrives by a follow-up update.
func TestAccDtcloudSnapshot_lifecycle(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-snap", "vol-0001", "nightly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_id", "vol-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "nightly copy"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					// Size comes from the source volume.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "size", "20"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "project_id", "proj-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "created_at", acctest.FakeCreatedAt),
					// The raw id, and the policy name resolved from it.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "storage_policy", "standard"),
					resource.TestCheckResourceAttrSet("dtcloud_snapshot.test", "id"),

					// The singular data source reads the same endpoint, description included.
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "description", "nightly copy"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "size", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "storage_policy", "standard"),

					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_id", "vol-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.size", "20"),
					// The list reports the id; the provider resolves the name, so the two
					// data sources cannot disagree.
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.storage_policy", "standard"),
					// updated_at is null until something is written.
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.created_at", acctest.FakeCreatedAt),

					resource.TestCheckResourceAttr("data.dtcloud_snapshots.by_volume", "snapshots.#", "1"),

					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						// Exactly one follow-up PUT for the description.
						if api.updates != 1 {
							return fmt.Errorf("expected one update call to set the description, saw %d", api.updates)
						}
						return nil
					},
				),
			},
			{
				// Nothing changed, so nothing may be planned.
				Config:   snapshotConfig(server.URL, "tf-acc-snap", "vol-0001", "nightly copy"),
				PlanOnly: true,
			},
			{
				// Rename alone: the fake holds the old name while the status never leaves
				// `available`. The description is untouched, so the next step is the mirror
				// image and neither half can cover for the other.
				Config: snapshotConfig(server.URL, "tf-acc-snap-renamed", "vol-0001", "nightly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap-renamed"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "nightly copy"),
					// Written on the first update.
					resource.TestCheckResourceAttrSet("dtcloud_snapshot.test", "updated_at"),
				),
			},
			{
				// Re-describe alone, for the same reason in reverse.
				Config: snapshotConfig(server.URL, "tf-acc-snap-renamed", "vol-0001", "weekly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap-renamed"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "weekly copy"),
				),
			},
			{
				// Everything round-trips, description included.
				ResourceName:      "dtcloud_snapshot.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_createWaitsForAvailable pins the one thing every other
// test here lets slip. It needs its own test: a configuration with a description
// has a second wait after create, behind which an early create wait is invisible.
func TestAccDtcloudSnapshot_createWaitsForAvailable(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: bareSnapshotConfig(server.URL, "tf-acc-wait", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					// The copy is finished, not merely started.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "progress", "100%"),
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_volumeIDForcesNew pins that pointing a snapshot at
// another volume is a replacement: nothing re-points an existing snapshot.
func TestAccDtcloudSnapshot_volumeIDForcesNew(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-forcenew", "vol-0001", ""),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "size", "20"),
			},
			{
				Config: snapshotConfig(server.URL, "tf-acc-forcenew", "vol-0002", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_id", "vol-0002"),
					// The new snapshot inherits the new volume's size, which is how a
					// replacement is told apart from an update.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "size", "40"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 2 {
							return fmt.Errorf("expected the volume change to force a second create, saw %d", api.creates)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_descriptionCanBeCleared pins the clear-by-null path: a
// description is removed only by an explicit null, an empty string is rejected,
// and an absent field leaves the value alone.
func TestAccDtcloudSnapshot_descriptionCanBeCleared(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", "written once"),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "description", "written once"),
			},
			{
				// Changing it to something else.
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", "written twice"),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "description", "written twice"),
			},
			{
				// Removing it from the configuration removes it on the platform.
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", ""),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, s := range api.snapshots {
							if s.Name == "tf-acc-desc" && s.Description != "" {
								return fmt.Errorf("the description was not cleared on the platform: %q", s.Description)
							}
						}
						return nil
					},
				),
			},
			{
				// And it stays cleared, rather than being proposed again.
				Config:   snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", ""),
				PlanOnly: true,
			},
			{
				// `description = ""` and no description argument both mean "no
				// description", so neither may error and neither may diff against the other.
				Config: acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name        = %q
  volume_id   = %q
  description = ""
}
`, "tf-acc-desc", "vol-0001"),
				PlanOnly: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_outOfBandDescriptionIsReconciled covers a description
// added outside Terraform where the configuration has none: it is removed. The
// explicit Destroy step matters — the framework's own teardown does not refresh.
func TestAccDtcloudSnapshot_outOfBandDescriptionIsReconciled(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	// The configuration never mentions a description; the platform grows one
	// underneath it.
	noDescription := bareSnapshotConfig(server.URL, "tf-acc-destroy", "vol-0001", "")

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: noDescription,
				// The mutation below is drift the next plan has to notice.
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", ""),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, s := range api.snapshots {
							if s.Name == "tf-acc-destroy" {
								s.Description = "added in the web console"
							}
						}
						return nil
					},
				),
			},
			{
				// Refresh sees it, the configuration has none, so it goes.
				Config: noDescription,
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, s := range api.snapshots {
						if s.Name == "tf-acc-destroy" && s.Description != "" {
							return fmt.Errorf("the out-of-band description survived: %q", s.Description)
						}
					}
					return nil
				},
			},
			{
				Config:  noDescription,
				Destroy: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_storagePolicyLookupIsBestEffort pins that resolving the
// policy name is a convenience: when it cannot be read the name is left empty.
func TestAccDtcloudSnapshot_storagePolicyLookupIsBestEffort(t *testing.T) {
	api := newFakeSnapshotAPI()
	api.policiesFail = true
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-nopolicy", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The raw id is still reported; only the name is missing.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "storage_policy", ""),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.storage_policy", ""),
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_deletedOutsideTerraform pins the not-found path: a
// snapshot removed elsewhere is dropped from state. The fake answers with the
// nested error shape, exercising the structured branch of dterr.IsNotFound.
func TestAccDtcloudSnapshot_deletedOutsideTerraform(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := bareSnapshotConfig(server.URL, "tf-acc-vanish", "vol-0001", "")

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				// The plan that follows is meant to propose creating it again.
				ExpectNonEmptyPlan: true,
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, s := range api.snapshots {
						if s.Name == "tf-acc-vanish" {
							s.deleted = true
						}
					}
					return nil
				},
			},
			{
				// The refresh fails to find it, so the plan proposes creating it again.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_rejectsHTMLInName keeps the character rule a plan-time
// error rather than a rejection halfway through an apply.
func TestAccDtcloudSnapshot_rejectsHTMLInName(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      snapshotConfig(server.URL, "tf-acc-<script>", "vol-0001", ""),
				ExpectError: regexp.MustCompile(`must not contain any of`),
			},
		},
	})
}
