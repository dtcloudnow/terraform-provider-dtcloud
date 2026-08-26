---
page_title: "dtcloud: dtcloud_ssh_key"
subcategory: "Account"
---

# dtcloud_ssh_key (Data Source)

Get information on an SSH key by name. This can be used to reference an existing
key (for example, its fingerprint) without managing it in Terraform.

## Example Usage

```hcl
data "dtcloud_ssh_key" "example" {
  name = "my-key-name"
}

output "key_fingerprint" {
  value = data.dtcloud_ssh_key.example.fingerprint
}
```

## Argument Reference

* `name` - (Required) The name of the SSH key.

## Attributes Reference

* `public_key` - The public key material.
* `fingerprint` - The fingerprint of the SSH key.
* `created_at` - When the SSH key was created, in RFC 3339 format.
* `user_id` - The ID of the user that owns the SSH key.
