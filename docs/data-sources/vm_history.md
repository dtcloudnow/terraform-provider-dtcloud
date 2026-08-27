---
page_title: "dtcloud: dtcloud_vm_history"
subcategory: "Compute"
---

# dtcloud_vm_history

Lists what has been done to a virtual machine: who did it, what it was, and when.

## Example Usage

```hcl
data "dtcloud_vm_history" "web" {
  vm_id = dtcloud_vm.web.id
}

output "recent_activity" {
  value = [
    for e in data.dtcloud_vm_history.web.entries :
    "${e.date_and_time}  ${e.activity}  (${e.initiator})"
  ]
}
```

## Argument Reference

* `vm_id` - (Required) ID of the virtual machine.

## Attributes Reference

* `entries` - Actions recorded against the VM, in the order the API reports them, each with:
  * `id` - ID of the entry, for use with `dtcloud_vm_history_entry`.
  * `date_and_time` - When it happened.
  * `activity` - What was done.
  * `initiator` - Who or what did it.

## Behaviour worth knowing

~> **Do not feed this into a resource argument.** The content changes every time anything
happens to the VM — including changes Terraform itself makes — so a resource that depends on it
would see a different value on every plan and never settle. Use it for outputs, for
`terraform console`, or for a check outside the dependency graph.

-> **There is no `status` here.** The list endpoint leaves it out; `dtcloud_vm_history_entry`
reads one entry in full and reports it. That is the only difference between the two.

-> This is an event log, so it exists only as a data source. There is nothing to create, change
or destroy, which is what a resource would need.
