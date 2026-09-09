---
page_title: "dtcloud: dtcloud_elastic_ips"
subcategory: "Networking"
---

# dtcloud_elastic_ips

Lists the elastic IPs allocated to the caller.

The question it is really for is *what are we paying for that nothing is using*. An allocated
address with nothing attached still occupies quota and still bills.

## Example Usage

```hcl
# Addresses nothing is using.
data "dtcloud_elastic_ips" "idle" {
  status = "DOWN"
}

output "idle_addresses" {
  value = data.dtcloud_elastic_ips.idle.ip_addresses
}

# Everything attached to a load balancer.
data "dtcloud_elastic_ips" "on_load_balancers" {
  assigned_type = "LB"
}
```

## Argument Reference

* `status` - (Optional) `ACTIVE`, `DOWN` or `ERROR`.
* `floating_network_id` - (Optional) Only addresses from this external network.
* `assigned_type` - (Optional) `VM`, `LB`, or `none` for addresses attached to nothing.
  `none` exists because the API reports "attached to nothing" as an empty string, which is
  not something you can write in a filter.

Filters are applied by the provider. The list route takes no query parameters, so every
address is fetched either way.

## Attributes Reference

* `elastic_ips` - The addresses that matched, each with `id`, `ip_address`, `status`,
  `floating_network_id`, `network_name`, `assigned_type`, `assigned_id`, `assigned_to` and
  `fixed_ip_address`.
* `ids` - Just the ids.
* `ip_addresses` - Just the addresses, in the same order as `ids`.

## Cost

The API builds every row of this list from three further calls of its own — it looks up all
networks, all load balancers and all virtual machines to resolve the names. It is not a cheap
read. When you already know which address you want, use the singular
[`dtcloud_elastic_ip`](elastic_ip.md) data source.

`ids` and `ip_addresses` follow the order the API returns. If your configuration depends on a
particular address rather than "all of them", look it up by address with the singular data
source — a list that grows by one changes every index in it.
