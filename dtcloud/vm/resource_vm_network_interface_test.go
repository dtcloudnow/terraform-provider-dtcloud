package vm_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// TestAccDtcloudVMNetworkInterface covers attach → read → in-place update of the
// security groups → import → detach.
func TestAccDtcloudVMNetworkInterface(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	iface := func(groups string) string {
		return fmt.Sprintf(`
resource "dtcloud_vm_network_interface" "extra" {
  vm_id           = dtcloud_vm.host.id
  network_id      = "99999999-8888-7777-6666-555555555555"
  security_groups = %s

  fixed_ip {
    ip_version = 4
  }
}
`, groups)
	}

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for _, v := range api.vms {
				if v.deleted {
					continue
				}
				for _, i := range v.ifaces {
					if strings.HasPrefix(i.PortID, "port-extra") {
						return fmt.Errorf("interface %s is still attached after destroy", i.PortID)
					}
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testVMAttachmentConfig(server.URL, iface(`["sg-a"]`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("dtcloud_vm_network_interface.extra", "port_id"),
					resource.TestCheckResourceAttrSet("dtcloud_vm_network_interface.extra", "primary_ip"),
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "security_groups.#", "1"),
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "security_groups.0", "sg-a"),
					// Same snapshot caveat as the volume test above.
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, v := range api.vms {
							if len(v.ifaces) != 2 {
								return fmt.Errorf("expected 2 interfaces on the VM (boot + attached), got %d", len(v.ifaces))
							}
						}
						return nil
					},
				),
			},
			{
				// security_groups is updatable, so this must not re-attach.
				Config: testVMAttachmentConfig(server.URL, iface(`["sg-a", "sg-b"]`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "security_groups.#", "2"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, v := range api.vms {
							extras := 0
							for _, i := range v.ifaces {
								if strings.HasPrefix(i.PortID, "port-extra") {
									extras++
								}
							}
							if extras != 1 {
								return fmt.Errorf("updating security groups should not re-attach; %d extra interfaces", extras)
							}
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "dtcloud_vm_network_interface.extra",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"fixed_ip"},
			},
		},
	})
}

// TestAccDtcloudVMNetworkInterface_matchesTheVMBlock pins the alignment between
// this resource and dtcloud_vm's `network` block: the same configuration has to
// be accepted by both. `fixed_ip` is Optional on both, and omitting it must
// still produce an address.
func TestAccDtcloudVMNetworkInterface_matchesTheVMBlock(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	noFixedIP := `
resource "dtcloud_vm_network_interface" "extra" {
  vm_id      = dtcloud_vm.host.id
  network_id = "99999999-8888-7777-6666-555555555555"
}
`
	pinned := `
resource "dtcloud_vm_network_interface" "extra" {
  vm_id      = dtcloud_vm.host.id
  network_id = "99999999-8888-7777-6666-555555555555"

  fixed_ip {
    ip_address = "10.0.1.77"
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testVMAttachmentConfig(server.URL, noFixedIP),
				Check: resource.ComposeAggregateTestCheckFunc(
					// An address was allocated even though none was asked for, and it was
					// written back into the block rather than only into primary_ip.
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "fixed_ip.#", "1"),
					resource.TestCheckResourceAttrSet("dtcloud_vm_network_interface.extra", "fixed_ip.0.ip_address"),
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "fixed_ip.0.ip_version", "4"),
					resource.TestCheckResourceAttrPair(
						"dtcloud_vm_network_interface.extra", "fixed_ip.0.ip_address",
						"dtcloud_vm_network_interface.extra", "primary_ip"),
				),
			},
			{
				// Pinning an address is an in-place change, not a replacement.
				Config: testVMAttachmentConfig(server.URL, pinned),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "fixed_ip.0.ip_address", "10.0.1.77"),
					resource.TestCheckResourceAttr("dtcloud_vm_network_interface.extra", "primary_ip", "10.0.1.77"),
				),
			},
		},
	})
}

// TestAccDtcloudVMNetworkInterface_concurrentAttachesGetTheirOwnPort is named
// after the rule it protects: two interfaces attached to one machine at the same
// time each end up holding the port that belongs to it.
//
// The attach endpoint does not report the port it created, so the new one is
// found by comparing the machine's interface list before and after. Terraform
// applies up to ten resources at once, so two attaches overlap and either
// comparison can see the other's port. The provider serialises them on the VM
// id; without that, both resources can adopt the same one.
func TestAccDtcloudVMNetworkInterface_concurrentAttachesGetTheirOwnPort(t *testing.T) {
	api := newFakeVMAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	extra := `
resource "dtcloud_vm_network_interface" "a" {
  vm_id      = dtcloud_vm.host.id
  network_id = "11111111-2222-3333-4444-555555555555"

  fixed_ip {
    ip_version = 4
  }
}

resource "dtcloud_vm_network_interface" "b" {
  vm_id      = dtcloud_vm.host.id
  network_id = "66666666-7777-8888-9999-000000000000"

  fixed_ip {
    ip_version = 4
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testVMAttachmentConfig(server.URL, extra),
				Check: func(s *terraform.State) error {
					a := s.RootModule().Resources["dtcloud_vm_network_interface.a"]
					b := s.RootModule().Resources["dtcloud_vm_network_interface.b"]
					if a == nil || b == nil {
						return fmt.Errorf("both interfaces should be in state")
					}
					pa, pb := a.Primary.Attributes["port_id"], b.Primary.Attributes["port_id"]
					if pa == "" || pb == "" {
						return fmt.Errorf("port_id is empty: a=%q b=%q", pa, pb)
					}
					if pa == pb {
						return fmt.Errorf("both interfaces adopted port %s; one of them is holding the other's", pa)
					}
					return nil
				},
			},
		},
	})
}
