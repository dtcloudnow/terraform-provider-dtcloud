package main

import (
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud"
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
)

func main() {
	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: dtcloud.Provider,
	})
}
