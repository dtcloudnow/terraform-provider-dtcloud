package config

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	dtgo "github.com/dtcloudnow/dt-go/v26"
)

// Config holds the raw provider-level settings from the Terraform provider block
// or their environment fallbacks, plus where to look for the configuration file.
type Config struct {
	AccessKey   string
	SecretKey   string
	APIEndpoint string
	RegionID    string

	// Both may be empty; see file.go for the defaults.
	ConfigFile string
	Profile    string
}

// CombinedConfig is passed to every CRUD function as `meta`. It wraps the
// dt-go client and caches lookups that are shared across a run.
type CombinedConfig struct {
	client *dtgo.Client

	policiesOnce sync.Once
	policies     map[string]string
}

// DTClient returns the underlying dt-go API client.
func (c *CombinedConfig) DTClient() *dtgo.Client { return c.client }

// StoragePolicyNames maps volume type id to policy name, read once per run.
// Best-effort: a failure caches an empty map.
func (c *CombinedConfig) StoragePolicyNames(ctx context.Context) map[string]string {
	c.policiesOnce.Do(func() {
		names := map[string]string{}
		if policies, _, err := c.client.Volume.ListStoragePolicies(ctx, nil); err == nil {
			for _, p := range policies {
				names[p.ID] = p.Name
			}
		}
		c.policies = names
	})
	return c.policies
}

// Client validates the configuration and builds an authenticated dt-go client.
// Values resolve highest-first: provider block, environment, configuration file.
// Access key, secret key and region are all mandatory. Warnings are returned
// rather than logged, for the caller to surface.
func (c *Config) Client() (client *CombinedConfig, warnings []string, err error) {
	accessKey, secretKey := c.AccessKey, c.SecretKey
	endpoint, regionID := c.APIEndpoint, c.RegionID

	// Only consulted when something is still missing, so a fully specified
	// provider block never depends on the home directory.
	path := "the configuration file"
	if accessKey == "" || secretKey == "" || regionID == "" || endpoint == "" {
		values, resolved, fileWarnings, err := Load(c.ConfigFile, c.Profile)
		if err != nil {
			return nil, fileWarnings, err
		}
		path = resolved
		warnings = fileWarnings

		accessKey = firstNonEmpty(accessKey, values.AccessKey)
		secretKey = firstNonEmpty(secretKey, values.SecretKey)
		endpoint = firstNonEmpty(endpoint, values.APIEndpoint)
		regionID = firstNonEmpty(regionID, values.RegionID)
	}

	if accessKey == "" || secretKey == "" {
		return nil, warnings, fmt.Errorf(
			"both `access_key` and `secret_key` must be set. Any one of these fixes it:\n\n"+
				"  1. Set this machine up once, and every project works with an empty provider block:\n\n"+
				"       %s\n\n"+
				"     It prompts, checks the credentials against the API, and writes\n"+
				"     %s\n\n"+
				"  2. Export them, which is what CI should do:\n\n"+
				"       DTCLOUD_ACCESS_KEY, DTCLOUD_SECRET_KEY, DTCLOUD_REGION_ID\n\n"+
				"  3. Put them in the provider block, ideally through variables so they stay\n"+
				"     out of version control.",
			setupCommand(), path)
	}
	if regionID == "" {
		return nil, warnings, fmt.Errorf(
			"`region_id` must be set. It is sent as serverId on every API call and the API\n"+
				"rejects requests without it. Set it in the provider block, export\n"+
				"DTCLOUD_REGION_ID, add `region_id` to %s, or run:\n\n"+
				"    %s",
			path, setupCommand())
	}

	opts := []dtgo.ClientOpt{dtgo.SetApiKey(accessKey, secretKey)}
	if endpoint != "" {
		opts = append(opts, dtgo.SetBaseURL(endpoint))
	}

	dtClient, err := dtgo.New(http.DefaultClient, opts...)
	if err != nil {
		return nil, warnings, fmt.Errorf("failed to initialize dt-go client: %w", err)
	}
	dtClient.ServerId = regionID

	return &CombinedConfig{client: dtClient}, warnings, nil
}

// setupCommand renders how to invoke this binary's setup command. The absolute
// path is used unless the name on PATH resolves to this same executable.
func setupCommand() string {
	const name = "terraform-provider-dtcloud"

	exe, err := os.Executable()
	if err != nil || exe == "" {
		return name + " configure"
	}
	if resolved, err := exec.LookPath(name); err == nil && sameFile(resolved, exe) {
		return name + " configure"
	}
	return exe + " configure"
}

// sameFile reports whether two paths are the same file on disk. Symlinks, short
// names on Windows and a trailing ".exe" all make equal files look different.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
