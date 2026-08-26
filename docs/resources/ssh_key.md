---
page_title: "dtcloud: dtcloud_ssh_key"
subcategory: "Account"
---

# dtcloud_ssh_key

Provides a dtcloud SSH key resource to allow you to manage SSH keys for server access.

## Example Usage

```hcl
resource "dtcloud_ssh_key" "default" {
  name       = "terraform-poc"
  public_key = file("~/.ssh/id_rsa.pub")
}
```

## Argument Reference

The following arguments are supported:

* `name` - (Required) The name of the SSH key for identification. Changing this forces a new resource to be created.
* `public_key` - (Required) The public key material. Changing this forces a new resource to be created.

~> **Note:** The API supports create, read, and delete only. There is no in-place
update, so changing either `name` or `public_key` recreates the key.

## Attributes Reference

In addition to the arguments above, the following attributes are exported:

* `fingerprint` - The fingerprint of the SSH key.
* `created_at` - When the SSH key was created, in RFC 3339 format.
* `user_id` - The ID of the user that owns the SSH key.

## Import

SSH keys can be imported using their `name`:

```
terraform import dtcloud_ssh_key.default my-key-name
```
