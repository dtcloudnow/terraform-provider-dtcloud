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

// TestAccDtcloudVMVolumeAttachment covers attach → read → import → detach, and
// checks the attachment shows up on the VM's own volume list.
func TestAccDtcloudVMVolumeAttachment(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const attachment = `
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.host.id
  volume_id = "vol-data-1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for _, v := range api.vms {
				for _, vol := range v.volumes {
					if vol.ID == "vol-data-1" && !v.deleted {
						return fmt.Errorf("volume vol-data-1 is still attached after destroy")
					}
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testVMAttachmentConfig(server.URL, attachment),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm_volume_attachment.data", "volume_id", "vol-data-1"),
					resource.TestCheckResourceAttr("dtcloud_vm_volume_attachment.data", "volume_name", "vol-data-1-name"),
					resource.TestCheckResourceAttr("dtcloud_vm_volume_attachment.data", "size", "50"),
					// dtcloud_vm.host's own `volume` list is a snapshot from its
					// last read, which happened before this attachment existed,
					// so assert against the API instead of the VM's state.
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, v := range api.vms {
							if len(v.volumes) != 2 {
								return fmt.Errorf("expected 2 volumes on the VM (boot + attached), got %d", len(v.volumes))
							}
						}
						return nil
					},
				),
			},
			{
				Config:   testVMAttachmentConfig(server.URL, attachment),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_vm_volume_attachment.data",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudVMNetworkInterface covers attach → read → in-place update of the
// security groups → import → detach.

// TestAccDtcloudVMVolumeAttachment_detachRevertIsReported is the rule that a
// refused detach is reported as a refusal.
//
// A detach is accepted with 202 and finished by the guest. When the guest will
// not release the filesystem the platform undoes it, and the volume goes
// `detaching` and then back to `in-use` — while never leaving the VM's volume
// list, which is what the wait used to watch. There was nothing to see there,
// so the apply sat until its timeout and then said `context deadline exceeded`,
// which names neither the cause nor the fix.
//
// The fake reverts exactly one detach, so the retry that follows this error
// behaves like the retry after an operator unmounts the filesystem — and the
// test framework's own cleanup can finish.
func TestAccDtcloudVMVolumeAttachment_detachRevertIsReported(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const attachment = `
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.host.id
  volume_id = "vol-data-1"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testVMAttachmentConfig(server.URL, attachment),
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.detachRevertsLeft = 1
					return nil
				},
			},
			{
				// Dropping the attachment from the configuration destroys only
				// that resource, which is the detach on its own.
				Config:      testVMAttachmentConfig(server.URL, ""),
				ExpectError: regexp.MustCompile(`returned to .in-use.*Unmount it in the guest, or stop`),
			},
		},
	})
}
