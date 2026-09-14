//go:build tools

// Package tools pins the version of the documentation generator so that every
// machine and every CI run generates docs with the same tool. Nothing here is
// compiled into the provider.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
