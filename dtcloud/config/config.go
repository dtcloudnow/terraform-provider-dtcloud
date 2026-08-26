package config

import (
	"fmt"
	"net/http"

	dtgo "github.com/dtcloudnow/dt-go"
)

// Config holds the raw provider-level settings collected from the Terraform
// provider block (or their environment-variable fallbacks).
type Config struct {
	AccessKey   string
	SecretKey   string
	APIEndpoint string
	RegionID    string
}

// CombinedConfig is the object handed to every resource/data-source CRUD
// function as `meta`. It wraps the configured dt-go client.
type CombinedConfig struct {
	client *dtgo.Client
}

// DTClient returns the underlying dt-go API client.
func (c *CombinedConfig) DTClient() *dtgo.Client { return c.client }

// Client validates the configuration and builds an authenticated dt-go client.
// Auth is the API access-key/secret-key header pair (x-api-access-key /
// x-api-secret-key), and RegionID is applied as the serverId sent on every
// request. All three are mandatory for the API to answer.
func (c *Config) Client() (*CombinedConfig, error) {
	if c.AccessKey == "" || c.SecretKey == "" {
		return nil, fmt.Errorf("both `access_key` and `secret_key` must be set (or DTCLOUD_ACCESS_KEY / DTCLOUD_SECRET_KEY)")
	}
	if c.RegionID == "" {
		return nil, fmt.Errorf("`region_id` must be set (or DTCLOUD_REGION_ID); it is sent as serverId on every API call")
	}

	opts := []dtgo.ClientOpt{dtgo.SetApiKey(c.AccessKey, c.SecretKey)}
	if c.APIEndpoint != "" {
		opts = append(opts, dtgo.SetBaseURL(c.APIEndpoint))
	}

	client, err := dtgo.New(http.DefaultClient, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize dt-go client: %w", err)
	}
	client.ServerId = c.RegionID

	return &CombinedConfig{client: client}, nil
}
