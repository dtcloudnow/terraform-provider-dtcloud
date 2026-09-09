package config

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	dtgo "github.com/dtcloudnow/dt-go"
)

// Config holds the raw provider-level settings collected from the Terraform
// provider block (or their environment-variable fallbacks), plus where to look
// for the configuration file that fills in whatever they leave out.
type Config struct {
	AccessKey   string
	SecretKey   string
	APIEndpoint string
	RegionID    string

	// ConfigFile and Profile select the file and the account within it. Both
	// may be empty, in which case the default path and the file's own
	// default_profile apply. See file.go.
	ConfigFile string
	Profile    string
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
}

// DTClient returns the underlying dt-go API client.
func (c *CombinedConfig) DTClient() *dtgo.Client { return c.client }

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
//
// Values are resolved highest-first: what the provider block says, then the
// environment, then the configuration file. Only settings still empty after the
// first two are taken from the file, which is what makes an environment
// variable a usable override in CI without editing anyone's home directory.
//
// Auth is the API access-key/secret-key header pair (x-api-access-key /
// x-api-secret-key), and RegionID is applied as the serverId sent on every
// request. All three are mandatory for the API to answer.
//
// Warnings are returned rather than logged so the caller can surface them as
// Terraform diagnostics; they are not failures.
func (c *Config) Client() (client *CombinedConfig, warnings []string, err error) {
	accessKey, secretKey := c.AccessKey, c.SecretKey
	endpoint, regionID := c.APIEndpoint, c.RegionID

	// The file is only consulted when something is still missing. Skipping it
	// otherwise is not just cheaper: it keeps a fully-specified provider block
	// from depending on whatever happens to be in the person's home directory,
	// which is what makes the acceptance tests reproducible on a machine that
	// has real credentials configured.
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

// setupCommand renders how to invoke this binary's own setup command.
//
// Terraform keeps the plugin under .terraform, so the bare name usually means
// nothing to a shell and the absolute path is the only thing that can be
// pasted. But when the binary *is* reachable by name — someone installed the
// release build, or put it on PATH — the short form is far friendlier, and a
// ninety-character path in an error message is its own small insult.
//
// So: check whether the name on PATH resolves to this very executable, and
// prefer the short form when it does.
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

// sameFile reports whether two paths are the same file on disk, which is more
// reliable than comparing strings: symlinks, 8.3 short names on Windows and a
// trailing ".exe" all make equal files look like different paths.
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
