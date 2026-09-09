---
page_title: "dtcloud: dtcloud_elastic_ip"
subcategory: "Networking"
---

# dtcloud_elastic_ip

Looks up one elastic IP, by id or by address.

Address lookup is the useful half. The address is the thing people know — it is in a DNS
record, or in someone else's firewall rule — while its id is written down nowhere outside the
platform.

## Example Usage

```hcl
data "dtcloud_elastic_ip" "public" {
  ip_address = "62.244.233.73"
}

output "currently_reaches" {
  value = "${data.dtcloud_elastic_ip.public.assigned_type}: ${data.dtcloud_elastic_ip.public.assigned_to}"
}
```

## Argument Reference

Exactly one of:

* `id` - (Optional) ID of the elastic IP.
* `ip_address` - (Optional) The public address. Addresses are unique, so this always resolves
  to at most one.

## Attributes Reference

* `id`, `ip_address` - whichever you did not give.
* `floating_network_id` - External network the address came from.
* `network_name` - That network's name, as the platform reports it.
* `status` - `ACTIVE` when associated, `DOWN` when not.
* `port_id` - Port the address is pointed at, empty when unassociated.
* `fixed_ip_address` - Private address the traffic is mapped to.
* `device_id` / `device_owner` - The resource behind the port and what kind of thing it is,
  read from the raw response.
* `assigned_id` / `assigned_to` / `assigned_type` - The same thing as the platform reports it:
  the id, the **name**, and `VM` or `LB`. `assigned_to` is the one worth reading — it is the
  only place a human-readable name for the machine appears.
* `router_id`, `description`, `created_at`, `updated_at`.

## Cost

This reads two endpoints. The list is where the network name and the assigned machine's name
come from, and the API builds each of its rows from three further calls of its own, so this is
not a free lookup. It is fine for a handful of addresses in a configuration; it is not
something to put inside a large `for_each`.
