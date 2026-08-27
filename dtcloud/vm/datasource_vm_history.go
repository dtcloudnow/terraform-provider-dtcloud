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

// DataSourceDtcloudVMHistory lists what has been done to a VM.
//
// This is an event log, not desired state, so it exists as a data source and
// could never be a resource: there is nothing to create, change or destroy.
//
// It is also the one kind of data source worth a warning. The content changes
// every time anything happens to the VM — including changes Terraform itself
// makes — so feeding it into a resource argument produces a value that differs
// on every plan and a resource that never settles. Use it for outputs, for
// `terraform console`, or for a check somewhere outside the dependency graph.
func DataSourceDtcloudVMHistory() *schema.Resource {
	return &schema.Resource{
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

// DataSourceDtcloudVMHistoryEntry reads one history entry in full.
//
// The list endpoint leaves out `status`; this is the only way to get it, and
// the only reason this data source exists separately rather than the list
// fetching details for every entry — that would be one call per entry.
func DataSourceDtcloudVMHistoryEntry() *schema.Resource {
	return &schema.Resource{
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
