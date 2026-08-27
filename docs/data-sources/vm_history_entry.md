---
page_title: "dtcloud: dtcloud_vm_history_entry"
subcategory: "Compute"
---

# dtcloud_vm_history_entry

Reads one entry from a virtual machine's history in full, including its outcome.

## Example Usage

```hcl
data "dtcloud_vm_history" "web" {
  vm_id = dtcloud_vm.web.id
}

# The most recent action, with its status.
data "dtcloud_vm_history_entry" "latest" {
  vm_id      = dtcloud_vm.web.id
  history_id = data.dtcloud_vm_history.web.entries[0].id
}

output "latest_action" {
  value = "${data.dtcloud_vm_history_entry.latest.activity}: ${data.dtcloud_vm_history_entry.latest.status}"
}
```

## Argument Reference

* `vm_id` - (Required) ID of the virtual machine.
* `history_id` - (Required) ID of the entry, from `dtcloud_vm_history`.

## Attributes Reference

* `date_and_time` - When it happened.
* `activity` - What was done.
* `initiator` - Who or what did it.
* `status` - Outcome of the action. **Reported only here** — the list endpoint leaves it out.

~> The same warning as `dtcloud_vm_history` applies: this is an event log, and its content
moves. Keep it out of resource arguments.
