package vm_test

import (
	"net/http/httptest"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// TestAccDtcloudVMs exercises the plural data source and its filters.
func TestAccDtcloudVMs(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := testVMAttachmentConfig(server.URL, `
data "dtcloud_vms" "all" {
  depends_on = [dtcloud_vm.host]
}
`)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_vms.all", "vms.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_vms.all", "vms.0.name", "tf-acc-host"),
					resource.TestCheckResourceAttr("data.dtcloud_vms.all", "vms.0.status", "ACTIVE"),
					resource.TestCheckResourceAttr("data.dtcloud_vms.all", "vms.0.ip_addresses.#", "1"),
					resource.TestCheckResourceAttrSet("data.dtcloud_vms.all", "id"),
				),
			},
		},
	})
}
