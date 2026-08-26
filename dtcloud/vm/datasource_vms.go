package vm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudVMs lists the VMs in the active region and project.
//
// The list endpoint returns a lighter record than the details endpoint — no
// image, no SSH key, no timestamps — so this exposes what a list actually
// carries. Use the singular `dtcloud_vm` data source for the full picture of
// one instance.
func DataSourceDtcloudVMs() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudVMsRead,
		Schema: map[string]*schema.Schema{
			"status": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return VMs in this status, e.g. ACTIVE or SHUTOFF.",
			},
			"task": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return VMs with this in-flight task.",
			},
			"security_group_name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return VMs attached to this security group.",
			},
			"vms": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The matching virtual machines.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":           {Type: schema.TypeString, Computed: true, Description: "ID of the virtual machine."},
						"name":         {Type: schema.TypeString, Computed: true, Description: "Name of the virtual machine."},
						"status":       {Type: schema.TypeString, Computed: true, Description: "Current state."},
						"task_state":   {Type: schema.TypeString, Computed: true, Description: "In-flight OpenStack task."},
						"ip_addresses": {Type: schema.TypeList, Computed: true, Elem: &schema.Schema{Type: schema.TypeString}, Description: "Addresses assigned to the VM."},
						"vcpus":        {Type: schema.TypeInt, Computed: true, Description: "Number of virtual CPUs."},
						"ram":          {Type: schema.TypeString, Computed: true, Description: "Memory as reported by the API."},
						"storage":      {Type: schema.TypeInt, Computed: true, Description: "Storage in GB."},
						"volume_size":  {Type: schema.TypeInt, Computed: true, Description: "Boot volume size in GB."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudVMsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	filter := struct {
		Status            string `url:"status,omitempty"`
		Task              string `url:"task,omitempty"`
		SecurityGroupName string `url:"securityGroupName,omitempty"`
	}{
		Status:            d.Get("status").(string),
		Task:              d.Get("task").(string),
		SecurityGroupName: d.Get("security_group_name").(string),
	}

	vms, _, err := client.VirtualMachine.ListVirtualMachines(ctx, filter)
	if err != nil {
		return diag.Errorf("Error listing VMs: %s", err)
	}

	out := make([]interface{}, 0, len(vms))
	ids := make([]string, 0, len(vms))
	for _, v := range vms {
		addresses := make([]interface{}, 0, len(v.IPAddress))
		for _, ip := range v.IPAddress {
			addresses = append(addresses, ip)
		}
		out = append(out, map[string]interface{}{
			"id":           v.ID,
			"name":         v.Name,
			"status":       v.Status,
			"task_state":   v.TaskState,
			"ip_addresses": addresses,
			"vcpus":        v.VCpus,
			"ram":          v.RAM,
			"storage":      v.Storage,
			"volume_size":  v.VolumeSize,
		})
		ids = append(ids, v.ID)
	}

	if err := d.Set("vms", out); err != nil {
		return diag.FromErr(err)
	}

	// A data source still needs an id. Hashing the members keeps it stable
	// between runs that return the same VMs, so an unchanged list does not show
	// up as a diff.
	d.SetId(hashIDs(ids))

	return nil
}

func hashIDs(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return fmt.Sprintf("%x", sum[:8])
}
