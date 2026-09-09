package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file implements the provider's configuration file: where it lives, what
// is in it, and how it is protected.
//
// # Why there is no "first run"
//
// A CLI can prompt. dtctl does exactly that — `dtctl auth init` asks for a key
// pair and writes it out. A Terraform provider cannot: it is a plugin that
// Terraform starts as a child process, frequently on a build agent with no
// terminal attached, and a prompt there is a hang rather than a question. So
// the file is never created by the provider. It is written by a person once,
// and from then on it is only ever read.
//
// That is also the established shape across the ecosystem: AWS, Google and
// Azure all read a credentials file their own CLI wrote — `aws configure`,
// `gcloud auth application-default login`, `az login` — and none of them offer
// to set one up from inside Terraform.
//
// What it does *not* do is read dtctl's configuration. The provider stands on
// its own: somebody using Terraform must not be made to install a CLI first.
// `terraform-provider-dtcloud configure` (see main.go) writes this file, and
// Terraform's own variable prompting covers the case where nobody wants a file
// at all.
//
// # Precedence
//
// Highest wins, and this ordering is the convention every major provider
// follows:
//
//  1. arguments in the `provider "dtcloud"` block
//  2. environment variables
//  3. this file
//  4. dt-go's built-in default, for the endpoint only
//
// The reason for that order is operational rather than aesthetic: a person's
// file is the convenient default, an environment variable is how CI overrides
// it without writing files, and an explicit argument is how one configuration
// reaches two accounts at once.

const (
	// ConfigDirName is the directory created under the OS's own configuration
	// home. Deliberately the full provider name, so it is obvious what wrote it
	// and it cannot be confused with dtctl's own directory next to it.
	ConfigDirName = "terraform-provider-dtcloud"

	// ConfigFileName matches dtctl's, as do the keys inside it.
	ConfigFileName = "config.yaml"

	// DefaultProfileName is used when nothing names a profile.
	DefaultProfileName = "default"

	// credentialFileMode is what the file is expected to be: readable by its
	// owner and by nobody else, and not writable even by the owner, because
	// nothing here ever writes to it.
	credentialFileMode fs.FileMode = 0o400
)

// FileValues is what a profile contributes. Empty strings mean "not set here",
// which lets a profile carry only a region while the keys come from elsewhere.
type FileValues struct {
	AccessKey   string
	SecretKey   string
	APIEndpoint string
	RegionID    string
}

// profile is one account's settings. The key names mirror dtctl's config so
// that the two files read the same way, and so that a dtctl config can be
// pointed at directly — see loadDocument.
type profile struct {
	API struct {
		AccessKey string `yaml:"access_key"`
		SecretKey string `yaml:"secret_key"`
		BaseURL   string `yaml:"base_url"`
	} `yaml:"api"`
	// Deliberately not a string: dtctl writes `region_id: 1` unquoted, so YAML
	// hands it over as an int, and a string field would fail the whole decode
	// with "cannot unmarshal !!int into string". Accepting either spelling is
	// what lets a dtctl config be used directly.
	RegionID any `yaml:"region_id"`
}

func (p profile) values() FileValues {
	return FileValues{
		AccessKey:   strings.TrimSpace(p.API.AccessKey),
		SecretKey:   strings.TrimSpace(p.API.SecretKey),
		APIEndpoint: strings.TrimSpace(p.API.BaseURL),
		RegionID:    scalarToString(p.RegionID),
	}
}

// scalarToString renders a YAML scalar that may have been written quoted or
// bare. A float is formatted without a trailing ".0", so `region_id: 2` and
// `region_id: "2"` both arrive as "2".
func scalarToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// document is the whole file. `profiles` is the normal shape; a document with
// no profiles at all is treated as a single unnamed profile, which is what
// makes dtctl's own config directly usable.
type document struct {
	DefaultProfile string             `yaml:"default_profile"`
	Profiles       map[string]profile `yaml:"profiles"`

	// Inline single-account form, identical in shape to dtctl's config.yaml.
	profile `yaml:",inline"`
}

// DefaultPath is where the file lives when nothing overrides it:
//
//	Linux    ~/.config/terraform-provider-dtcloud/config.yaml
//	macOS    ~/Library/Application Support/terraform-provider-dtcloud/config.yaml
//	Windows  %AppData%\terraform-provider-dtcloud\config.yaml
//
// os.UserConfigDir is what resolves that per platform, and it is the same call
// dtctl makes, so the two directories always sit side by side.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not determine this machine's configuration directory: %w", err)
	}
	return filepath.Join(dir, ConfigDirName, ConfigFileName), nil
}

// Load reads one profile out of the configuration file.
//
// path may be empty, in which case DefaultPath is used and a missing file is
// not an error — the provider simply has nothing to contribute from here, and
// arguments or environment variables are expected to supply everything. An
// explicitly named path that does not exist *is* an error: asking for a
// specific file and silently getting none of it is the kind of quiet failure
// that costs an afternoon.
//
// It returns the resolved path and any warnings, so the caller can surface them
// as Terraform diagnostics rather than swallowing them.
func Load(path, profileName string) (values FileValues, resolved string, warnings []string, err error) {
	explicit := path != ""
	if !explicit {
		path, err = DefaultPath()
		if err != nil {
			return FileValues{}, "", nil, err
		}
	}
	path = expandHome(path)

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return FileValues{}, path, nil, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return FileValues{}, path, nil, fmt.Errorf(
				"configuration file %s does not exist. Create it, or drop `config_file` / DTCLOUD_CONFIG_FILE to fall back to %s",
				path, mustDefaultPath())
		}
		return FileValues{}, path, nil, fmt.Errorf("could not read configuration file %s: %w", path, err)
	}
	if info.IsDir() {
		return FileValues{}, path, nil, fmt.Errorf("configuration file %s is a directory, not a file", path)
	}

	// A file the practitioner named explicitly is read, warned about and never
	// altered: it may belong to another tool that still needs to write to it.
	warnings = append(warnings, enforcePermissions(path, info, explicit)...)

	raw, err := os.ReadFile(path)
	if err != nil {
		return FileValues{}, path, warnings, fmt.Errorf("could not read configuration file %s: %w", path, err)
	}

	values, err = loadDocument(raw, path, profileName)
	return values, path, warnings, err
}

func loadDocument(raw []byte, path, profileName string) (FileValues, error) {
	var doc document
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return FileValues{}, fmt.Errorf("configuration file %s is not valid YAML: %w", path, err)
	}

	// No `profiles` block: the whole document is one account, which is exactly
	// the shape dtctl writes. Naming a profile in that case is a mistake worth
	// reporting rather than ignoring.
	if len(doc.Profiles) == 0 {
		if profileName != "" && profileName != DefaultProfileName {
			return FileValues{}, fmt.Errorf(
				"configuration file %s has no `profiles` block, so profile %q cannot be selected. "+
					"Either remove the profile setting, or restructure the file with a `profiles:` map",
				path, profileName)
		}
		return doc.profile.values(), nil
	}

	if profileName == "" {
		profileName = strings.TrimSpace(doc.DefaultProfile)
	}
	if profileName == "" {
		profileName = DefaultProfileName
	}

	p, ok := doc.Profiles[profileName]
	if !ok {
		return FileValues{}, fmt.Errorf(
			"configuration file %s has no profile %q. It defines: %s",
			path, profileName, strings.Join(profileNames(doc.Profiles), ", "))
	}
	return p.values(), nil
}

func profileNames(profiles map[string]profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// enforcePermissions keeps the file readable by its owner and nobody else.
//
// The file holds an API secret key, so the standard applies that applies to an
// SSH private key: if anyone else on the machine can read it, it is not a
// secret. This tightens a file that is too open rather than only complaining
// about one, because a warning nobody reads protects nothing.
//
// It never *widens* permissions, and it only tightens the provider's own file —
// the one at the default path. A file the practitioner pointed at explicitly is
// left alone apart from a warning, because it may well be dtctl's config, and
// dtctl rewrites that file whenever the key changes; making it read-only would
// break the CLI to tidy up the provider.
//
// On Windows, Go's os.Chmod maps this onto the read-only attribute, which is
// the closest equivalent the platform offers. There are no POSIX mode bits
// there to inspect, so the check does not run.
func enforcePermissions(path string, info fs.FileInfo, explicit bool) []string {
	if runtime.GOOS == "windows" {
		return nil
	}

	mode := info.Mode().Perm()
	tighten, tooOpen := permissionAction(mode, explicit)
	if !tighten && !tooOpen {
		return nil
	}

	if !tighten {
		return []string{fmt.Sprintf(
			"Configuration file %s is readable by other users (mode %#o). It holds an API secret key. "+
				"Run `chmod 400 %s`. It was not changed automatically because it was named explicitly and "+
				"may be shared with another tool.", path, mode, path)}
	}

	if err := os.Chmod(path, credentialFileMode); err != nil {
		return []string{fmt.Sprintf(
			"Configuration file %s is mode %#o and could not be tightened to 0400: %s. "+
				"It holds an API secret key; run `chmod 400 %s`.", path, mode, err, path)}
	}
	if tooOpen {
		return []string{fmt.Sprintf(
			"Configuration file %s was mode %#o, readable by other users, and has been tightened to 0400. "+
				"It holds an API secret key.", path, mode)}
	}
	return nil
}

// permissionAction is the decision behind enforcePermissions, separated from
// the file I/O so the rule can be tested on any operating system — the chmod
// itself only means something on a POSIX filesystem.
//
// tighten says the provider should reset the file to 0400; tooOpen says other
// users can currently read it, which is always worth reporting.
func permissionAction(mode fs.FileMode, explicit bool) (tighten, tooOpen bool) {
	tooOpen = mode&0o077 != 0
	if mode&0o077 == 0 && mode&0o200 == 0 {
		return false, false // already 0400 or tighter
	}
	// A file named explicitly may belong to another tool — dtctl rewrites its
	// own config whenever the key changes — so it is reported, never altered.
	return !explicit, tooOpen
}

// expandHome resolves a leading ~ so that a path written in a .tf file works
// the way a path written in a shell does.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path[1:], string(os.PathSeparator)))
}

// mustDefaultPath renders the default location for an error message, falling
// back to a description when the OS will not say where it is.
func mustDefaultPath() string {
	p, err := DefaultPath()
	if err != nil {
		return filepath.Join("<user config dir>", ConfigDirName, ConfigFileName)
	}
	return p
}
