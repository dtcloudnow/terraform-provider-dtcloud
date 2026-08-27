---
page_title: "dtcloud: dtcloud_ssh_keys"
subcategory: "Compute"
---

# dtcloud_ssh_keys

Lists the SSH keys on the account.

## Example Usage

```hcl
data "dtcloud_ssh_keys" "all" {}

output "key_names" {
  value = data.dtcloud_ssh_keys.all.names
}
```

Only create a key if one with that name is not already there:

```hcl
data "dtcloud_ssh_keys" "all" {}

resource "dtcloud_ssh_key" "default" {
  count = contains(data.dtcloud_ssh_keys.all.names, "my-key") ? 0 : 1

  name       = "my-key"
  public_key = file("~/.ssh/id_rsa.pub")
}
```

## Attributes Reference

* `ssh_keys` - The keys, each with `name` and `created`.
* `names` - Just the names, for the common case of checking whether a key exists.

~> **The list endpoint reports a name and a creation timestamp, nothing else.** Fingerprints
and public keys come only from the details endpoint, so look a key up by name with the
`dtcloud_ssh_key` data source when you need those. Fetching them here would mean one extra call
per key.
