package vm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudVMHistory lists what has been done to a VM: an event log
// rather than desired state, so there is nothing to create or destroy. Its
// content changes whenever anything happens to the VM, Terraform's own changes
// included, so use it for outputs rather than for a resource argument.
func DataSourceDtcloudVMHistory() *schema.Resource {
	return &schema.Resource{
		Description: "Lists what has been done to a virtual machine.\n\n" +
			"This is an event log rather than desired state, which is why it is a data source and could " +
			"never be a resource: there is nothing to create, change or destroy.\n\n" +
			"It is worth a warning. The content changes every time anything happens to the machine -- " +
			"including changes Terraform itself makes -- so feeding it into a resource argument produces " +
			"a value that differs on every plan and a resource that never settles. Use it for outputs, " +
			"for `terraform console`, or for a check outside the dependency graph.",

		ReadContext: dataSourceDtcloudVMHistoryRead,
		Schema: map[string]*schema.Schema{
			"vm_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the virtual machine.",
			},
			"entries": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Actions recorded against the VM, newest first as the API reports them.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":            {Type: schema.TypeString, Computed: true, Description: "ID of the entry, for use with dtcloud_vm_history_entry."},
						"date_and_time": {Type: schema.TypeString, Computed: true, Description: "When it happened, as reported by the API."},
						"activity":      {Type: schema.TypeString, Computed: true, Description: "What was done."},
						"initiator":     {Type: schema.TypeString, Computed: true, Description: "Who or what did it."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudVMHistoryRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	vmID := d.Get("vm_id").(string)

	history, _, err := client.VirtualMachine.GetVmHistory(ctx, vmID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("VM %q not found", vmID)
		}
		return diag.Errorf("Error retrieving the history of VM %q: %s", vmID, err)
	}

	out := make([]interface{}, 0, len(history))
	ids := make([]string, 0, len(history))
	for _, e := range history {
		out = append(out, map[string]interface{}{
			"id":            e.ID,
			"date_and_time": e.DateAndTime,
			"activity":      e.Activity,
			"initiator":     e.Initiator,
		})
		ids = append(ids, e.ID)
	}

	if err := d.Set("entries", out); err != nil {
		return diag.FromErr(err)
	}

	sum := sha256.Sum256([]byte(vmID + "|" + strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}

// DataSourceDtcloudVMHistoryEntry reads one history entry in full. The list
// endpoint leaves out `status`, and fetching it per entry would cost a call each.
func DataSourceDtcloudVMHistoryEntry() *schema.Resource {
	return &schema.Resource{
		Description: "Reads one virtual machine history entry in full.\n\n" +
			"The list endpoint leaves out `status`; this is the only way to get it. That is also why this " +
			"is a separate data source rather than the list fetching details for every entry, which would " +
			"be one call per entry.",

		ReadContext: dataSourceDtcloudVMHistoryEntryRead,
		Schema: map[string]*schema.Schema{
			"vm_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the virtual machine.",
			},
			"history_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the entry, from dtcloud_vm_history.",
			},
			"date_and_time": {Type: schema.TypeString, Computed: true, Description: "When it happened."},
			"activity":      {Type: schema.TypeString, Computed: true, Description: "What was done."},
			"initiator":     {Type: schema.TypeString, Computed: true, Description: "Who or what did it."},
			"status":        {Type: schema.TypeString, Computed: true, Description: "Outcome of the action. Reported only by this endpoint, not by the list."},
		},
	}
}

func dataSourceDtcloudVMHistoryEntryRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	vmID := d.Get("vm_id").(string)
	historyID := d.Get("history_id").(string)

	entry, _, err := client.VirtualMachine.GetVmHistoryDetails(ctx, vmID, historyID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("History entry %q not found on VM %q", historyID, vmID)
		}
		return diag.Errorf("Error retrieving history entry %q of VM %q: %s", historyID, vmID, err)
	}

	d.SetId(vmID + ":" + historyID)
	d.Set("date_and_time", entry.DateAndTime)
	d.Set("activity", entry.Activity)
	d.Set("initiator", entry.Initiator)
	d.Set("status", entry.Status)

	return nil
}
