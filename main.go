package main

import (
	"os"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud"
	"github.com/dtcloudnow/terraform-provider-dtcloud/internal/setup"
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
)

func main() {
	// Terraform runs this binary with no arguments and talks to it over gRPC.
	// A recognised first argument means a person ran it directly instead, which
	// is how the provider offers a setup command without anyone having to
	// install a second tool. Anything unrecognised falls through to the plugin,
	// so Terraform's own flags are never intercepted.
	if len(os.Args) > 1 && (os.Args[1] == "configure" || os.Args[1] == "-configure") {
		os.Exit(setup.Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: dtcloud.Provider,
	})
}
