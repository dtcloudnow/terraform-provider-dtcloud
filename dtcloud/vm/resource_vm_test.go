package vm_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// TestAccDtcloudVM_lifecycle drives create (including the wait for ACTIVE) →
// read → re-plan → in-place rename → ForceNew on flavor → import → destroy.
func TestAccDtcloudVM_lifecycle(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, v := range api.vms {
				if !v.deleted {
					return fmt.Errorf("VM %s still exists in the API after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testVMConfig(server.URL, "tf-acc-vm", "flavor-small"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm.test", "name", "tf-acc-vm"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "flavor_id", "flavor-small"),
					// Proves the create waiter ran: the first read reported BUILD, so
					// ACTIVE here means it polled until settled.
					resource.TestCheckResourceAttr("dtcloud_vm.test", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "image", fakeVMImage),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "image_os_type", fakeVMOsType),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "ssh_key", fakeVMKeyName),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "flavor_name", "flavor-small"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "vcpus", "1"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "ram", "512 MB"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "created_at", "2026-07-08T10:35:17Z"),

					// hotPlugEnabled and metadata come from the raw body; dt-go's typed
					// details struct carries neither.
					resource.TestCheckResourceAttr("dtcloud_vm.test", "enable_hot_plug", "false"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "metadata.ha_enabled", "true"),

					// The event log. `status` is missing from the list and only the
					// detail endpoint has it, which is why there are two.
					resource.TestCheckResourceAttr("data.dtcloud_vm_history.test", "entries.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_vm_history.test", "entries.0.activity", "Start"),
					resource.TestCheckResourceAttr("data.dtcloud_vm_history.test", "entries.1.id", "hist-1"),
					resource.TestCheckNoResourceAttr("data.dtcloud_vm_history.test", "entries.0.status"),
					resource.TestCheckResourceAttr("data.dtcloud_vm_history_entry.first", "activity", "Create"),
					resource.TestCheckResourceAttr("data.dtcloud_vm_history_entry.first", "status", "Success"),

					// Read pulls these from their own endpoints, not from details.
					resource.TestCheckResourceAttr("dtcloud_vm.test", "primary_ip", "10.0.0.10"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "network_interface.#", "1"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "network_interface.0.primary_ip", "10.0.0.10"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "network_interface.0.is_public", "true"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "volume.#", "1"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "volume.0.size", "20"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "state", "running"),

					resource.TestCheckResourceAttr("data.dtcloud_vm.test", "primary_ip", "10.0.0.10"),
					resource.TestCheckResourceAttr("data.dtcloud_vm.test", "name", "tf-acc-vm"),
					resource.TestCheckResourceAttr("data.dtcloud_vm.test", "flavor_name", "flavor-small"),

					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if len(api.createdNames) != 1 {
							return fmt.Errorf("expected exactly 1 VM to be created, got %d", len(api.createdNames))
						}
						return nil
					},
				),
			},
			{
				// Re-applying the same config must be a no-op.
				Config:   testVMConfig(server.URL, "tf-acc-vm", "flavor-small"),
				PlanOnly: true,
			},
			{
				// name is updatable, so this must NOT recreate the VM.
				Config: testVMConfig(server.URL, "tf-acc-vm-renamed", "flavor-small"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm.test", "name", "tf-acc-vm-renamed"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.renames != 1 {
							return fmt.Errorf("expected 1 rename call, got %d", api.renames)
						}
						if len(api.createdNames) != 1 {
							return fmt.Errorf("rename should not have created a VM; %d creates seen", len(api.createdNames))
						}
						return nil
					},
				),
			},
			{
				// flavor_id now resizes in place; the VM must NOT be recreated.
				Config: testVMConfig(server.URL, "tf-acc-vm-renamed", "flavor-large") + vmOutputs,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm.test", "flavor_id", "flavor-large"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "flavor_name", "flavor-large"),
					// Checked straight after the apply, before any refresh could paper
					// over it. Terraform plans a computed attribute as unchanged unless
					// CustomizeDiff marks it unknown, so without that these would still
					// hold the old flavor's numbers.
					resource.TestCheckResourceAttr("dtcloud_vm.test", "vcpus", "4"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "ram", "8192 MB"),
					resource.TestCheckOutput("vm_vcpus", "4"),
					resource.TestCheckOutput("vm_ram", "8192 MB"),
					// The VM must end up running again: resizeVM stops it because the
					// platform requires that, and the power step restores the configured
					// state afterwards.
					resource.TestCheckResourceAttr("dtcloud_vm.test", "status", "ACTIVE"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.resizes != 1 {
							return fmt.Errorf("expected 1 resize call, got %d", api.resizes)
						}
						if len(api.createdNames) != 1 {
							return fmt.Errorf("resize must not recreate the VM; %d creates seen", len(api.createdNames))
						}
						// stop before the resize, start after it.
						if len(api.powerOps) < 2 {
							return fmt.Errorf("resize should have stopped then started the VM, got %v", api.powerOps)
						}
						last2 := api.powerOps[len(api.powerOps)-2:]
						if last2[0] != "softStop" || last2[1] != "start" {
							return fmt.Errorf("expected softStop then start around the resize, got %v", api.powerOps)
						}
						return nil
					},
				),
			},
			{
				// state drives the power actions.
				Config: testVMConfigState(server.URL, "tf-acc-vm-renamed", "flavor-large", "stopped"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm.test", "state", "stopped"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "status", "SHUTOFF"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if len(api.powerOps) == 0 || api.powerOps[len(api.powerOps)-1] != "softStop" {
							return fmt.Errorf("expected a softStop (graceful_shutdown defaults to true), got %v", api.powerOps)
						}
						return nil
					},
				),
			},
			{
				// and back on again
				Config: testVMConfigState(server.URL, "tf-acc-vm-renamed", "flavor-large", "running"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm.test", "state", "running"),
					resource.TestCheckResourceAttr("dtcloud_vm.test", "status", "ACTIVE"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.powerOps[len(api.powerOps)-1] != "start" {
							return fmt.Errorf("expected a start, got %v", api.powerOps)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_vm.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Only these are readable back from the details endpoint. key_name and
				// network are deliberately not ignored: both are recovered from the API,
				// and this step is what proves it. The rest cannot be — block_device
				// because the image id is never reported and image names are not unique,
				// user_data and script because nothing echoes them back, and the last
				// three because they only exist in configuration.
				ImportStateVerifyIgnore: []string{
					"block_device", "user_data", "script",
					"is_gpu_image", "graceful_shutdown",
				},
			},
		},
	})
}

// TestAccDtcloudVM_createError checks that a VM landing in ERROR fails the
// apply promptly instead of blocking until the create timeout.
func TestAccDtcloudVM_createError(t *testing.T) {
	api := newFakeVMAPI()
	api.buildPolls = 0
	api.failWithError = true
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testVMConfig(server.URL, "tf-acc-vm-broken", "flavor-small"),
				ExpectError: regexp.MustCompile("entered ERROR state: Exceeded maximum number of retries"),
			},
		},
	})
}

// TestAccDtcloudVM_userDataAndScript pins a rule no schema can express:
// cloud-init is built from `script` only when `user_data` is absent, so setting
// both discards the script without a word.
func TestAccDtcloudVM_userDataAndScript(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_vm" "both" {
  name      = "tf-acc-both"
  flavor_id = "flavor-small"
  user_data = "I2Nsb3VkLWNvbmZpZwo="

  script {
    os       = "linux"
    password = "hunter2"
  }

  network {
    uuid            = "11111111-2222-3333-4444-555555555555"
    security_groups = ["sg-default"]

    fixed_ip {
      ip_version = 4
    }
  }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = "img-0001"
  }
}
`,
				ExpectError: regexp.MustCompile("user_data and script cannot both be set"),
			},
		},
	})
}
