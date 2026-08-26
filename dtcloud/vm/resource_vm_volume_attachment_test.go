package vm_test

import (
	"fmt"
	"net/http/httptest"
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
