package config

import (
	"strings"
	"testing"
)

// TestPrecedence pins the ordering every major Terraform provider uses:
// provider block, then environment, then file. Getting this backwards is the
// kind of thing nobody notices until CI quietly uses a developer's own account.
func TestPrecedence(t *testing.T) {
	path := writeConfig(t, `
api:
  access_key: file-access
  secret_key: file-secret
  base_url: https://file.example/api/v1
region_id: 9
`)

	t.Run("the file supplies what nothing else does", func(t *testing.T) {
		c := Config{ConfigFile: path}
		client, _, err := c.Client()
		if err != nil {
			t.Fatalf("Client: %s", err)
		}
		if got := client.DTClient().ServerId; got != "9" {
			t.Errorf("region = %q, want 9 from the file", got)
		}
		if got := client.DTClient().BaseURL.String(); got != "https://file.example/api/v1" {
			t.Errorf("endpoint = %q, want the file's", got)
		}
	})

	t.Run("the provider block beats the file", func(t *testing.T) {
		c := Config{
			ConfigFile:  path,
			AccessKey:   "block-access",
			SecretKey:   "block-secret",
			RegionID:    "3",
			APIEndpoint: "https://block.example/api/v1",
		}
		client, _, err := c.Client()
		if err != nil {
			t.Fatalf("Client: %s", err)
		}
		if got := client.DTClient().ServerId; got != "3" {
			t.Errorf("region = %q, want 3 from the provider block", got)
		}
		if got := client.DTClient().BaseURL.String(); got != "https://block.example/api/v1" {
			t.Errorf("endpoint = %q, want the block's", got)
		}
	})

	t.Run("settings mix: the file fills only the gaps", func(t *testing.T) {
		// The realistic case — keys in the file, region overridden for one run.
		c := Config{ConfigFile: path, RegionID: "3"}
		client, _, err := c.Client()
		if err != nil {
			t.Fatalf("Client: %s", err)
		}
		if got := client.DTClient().ServerId; got != "3" {
			t.Errorf("region = %q, want the override", got)
		}
		if got := client.DTClient().BaseURL.String(); got != "https://file.example/api/v1" {
			t.Errorf("endpoint = %q, want the file's — it was not overridden", got)
		}
	})
}

// The first thing most people will ever see from this provider is one of these
// two errors, so each has to say what to do about it.
func TestMissingSettingsExplainThemselves(t *testing.T) {
	isolateConfigHome(t)

	t.Run("no credentials", func(t *testing.T) {
		_, _, err := (&Config{RegionID: "1"}).Client()
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"access_key", "DTCLOUD_ACCESS_KEY", ConfigDirName} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q: %s", want, err)
			}
		}
	})

	t.Run("no region", func(t *testing.T) {
		_, _, err := (&Config{AccessKey: "a", SecretKey: "b"}).Client()
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"region_id", "DTCLOUD_REGION_ID", "serverId"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q: %s", want, err)
			}
		}
	})

	// The one that used to succeed. An omitted endpoint let the SDK pick one,
	// so a configuration with a typo'd or forgotten endpoint built resources in
	// whichever environment that default named, reporting nothing.
	t.Run("no endpoint", func(t *testing.T) {
		_, _, err := (&Config{AccessKey: "a", SecretKey: "b", RegionID: "1"}).Client()
		if err == nil {
			t.Fatal("an empty endpoint must be refused, not filled in by the SDK")
		}
		for _, want := range []string{"api_endpoint", "DTCLOUD_API_URL", "base_url"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q: %s", want, err)
			}
		}
	})
}

// Whitespace around a pasted key is invisible and would otherwise produce a
// 401 that looks like a wrong key rather than a stray space.
func TestValuesAreTrimmed(t *testing.T) {
	path := writeConfig(t, "api:\n  access_key: \"  padded-access  \"\n  secret_key: \"  padded-secret  \"\n  base_url: \"  https://padded.example/api/v1  \"\nregion_id: \"  4  \"\n")
	client, _, err := (&Config{ConfigFile: path}).Client()
	if err != nil {
		t.Fatalf("Client: %s", err)
	}
	if got := client.DTClient().ServerId; got != "4" {
		t.Errorf("region = %q, want it trimmed to 4", got)
	}
	if got := client.DTClient().BaseURL.String(); got != "https://padded.example/api/v1" {
		t.Errorf("endpoint = %q, want it trimmed", got)
	}
}
