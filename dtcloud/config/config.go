package config

import (
	"context"
	"fmt"
	"net/http"
	"sync"

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
//
// Every CRUD call in a run shares one of these, which makes it the right place
// for anything worth reading once instead of once per resource.
type CombinedConfig struct {
	client *dtgo.Client

	policiesOnce sync.Once
	policies     map[string]string

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// DTClient returns the underlying dt-go API client.
func (c *CombinedConfig) DTClient() *dtgo.Client { return c.client }

// Lock serialises the resources that share a key, and Unlock releases it.
//
// Some endpoints apply a change by reading a list, editing it and writing the
// whole list back. Two resources acting on the same parent at the same time
// both read the old list, and the second write undoes the first — Terraform
// applies up to ten resources in parallel by default, so this is the ordinary
// case rather than a rare one. Resources built on such an endpoint take this
// lock on the parent's id for the whole read-modify-write.
//
// The key is a caller's choice; use one that names the object being rewritten,
// such as the router id behind a static route.
func (c *CombinedConfig) Lock(key string) {
	c.mutexFor(key).Lock()
}

// Unlock releases the lock taken by Lock for the same key.
func (c *CombinedConfig) Unlock(key string) {
	c.mutexFor(key).Unlock()
}

func (c *CombinedConfig) mutexFor(key string) *sync.Mutex {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	if _, ok := c.locks[key]; !ok {
		c.locks[key] = &sync.Mutex{}
	}
	return c.locks[key]
}

// StoragePolicyNames maps volume type id to storage policy name, read at most
// once per Terraform run.
//
// The snapshot endpoints report a volume type id where the rest of the provider
// reports a policy name. Resolving that per snapshot would repeat the same call
// for the same answer, since volume types do not change during an apply.
//
// Best-effort: a failure caches an empty map rather than an error, so a caller
// that only wanted a display name degrades to not having one instead of failing
// a read that otherwise succeeded. Callers needing the difference should call
// ListStoragePolicies themselves.
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
