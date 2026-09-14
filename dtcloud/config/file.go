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

// The provider's configuration file, only ever read here; the `configure`
// subcommand writes it. Precedence, highest first: provider block, environment,
// this file, then dt-go's built-in default for the endpoint.

const (
	// ConfigDirName is the directory created under the OS's configuration home.
	// The full provider name, so it cannot be confused with dtctl's beside it.
	ConfigDirName = "terraform-provider-dtcloud"

	// ConfigFileName matches dtctl's, as do the keys inside it.
	ConfigFileName = "config.yaml"

	// DefaultProfileName is used when nothing names a profile.
	DefaultProfileName = "default"

	// credentialFileMode: readable by its owner and by nobody else, and not
	// writable, because nothing here ever writes to it.
	credentialFileMode fs.FileMode = 0o400
)

// FileValues is what a profile contributes. Empty strings mean "not set here",
// so a profile can carry only a region while the keys come from elsewhere.
type FileValues struct {
	AccessKey   string
	SecretKey   string
	APIEndpoint string
	RegionID    string
}

// profile is one account's settings. The key names mirror dtctl's config, so a
// dtctl config can be pointed at directly — see loadDocument.
type profile struct {
	API struct {
		AccessKey string `yaml:"access_key"`
		SecretKey string `yaml:"secret_key"`
		BaseURL   string `yaml:"base_url"`
	} `yaml:"api"`
	// Not a string: an unquoted `region_id: 1` decodes as an int and would fail
	// the whole document. This accepts either spelling.
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

// scalarToString renders a YAML scalar written quoted or bare. A float loses
// its trailing ".0", so `region_id: 2` and `region_id: "2"` both arrive as "2".
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

// document is the whole file. A document with no profiles at all is treated as
// a single unnamed profile, which is what makes dtctl's own config usable.
type document struct {
	DefaultProfile string             `yaml:"default_profile"`
	Profiles       map[string]profile `yaml:"profiles"`

	// Inline single-account form, identical in shape to dtctl's config.yaml.
	profile `yaml:",inline"`
}

// DefaultPath is config.yaml under os.UserConfigDir:
//
//	Linux    ~/.config/terraform-provider-dtcloud/config.yaml
//	macOS    ~/Library/Application Support/terraform-provider-dtcloud/config.yaml
//	Windows  %AppData%\terraform-provider-dtcloud\config.yaml
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not determine this machine's configuration directory: %w", err)
	}
	return filepath.Join(dir, ConfigDirName, ConfigFileName), nil
}

// Load reads one profile out of the configuration file, returning the resolved
// path and any warnings. An empty path means DefaultPath, where a missing file
// is not an error; a path named explicitly that does not exist is.
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

	// No `profiles` block: the whole document is one account. Naming a profile in
	// that case is a mistake worth reporting rather than ignoring.
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

// enforcePermissions resets the file to 0400 when others can read it, and never
// widens. Only the default path is tightened; an explicitly named file gets a
// warning instead. Skipped on Windows.
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

// permissionAction is the decision behind enforcePermissions, split out so the
// rule can be tested on any OS.
func permissionAction(mode fs.FileMode, explicit bool) (tighten, tooOpen bool) {
	tooOpen = mode&0o077 != 0
	if mode&0o077 == 0 && mode&0o200 == 0 {
		return false, false // already 0400 or tighter
	}
	// A file named explicitly may belong to another tool, so it is reported,
	// never altered.
	return !explicit, tooOpen
}

// expandHome resolves a leading ~ so a path written in a .tf file behaves like
// one written in a shell.
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

// mustDefaultPath renders the default location for an error message.
func mustDefaultPath() string {
	p, err := DefaultPath()
	if err != nil {
		return filepath.Join("<user config dir>", ConfigDirName, ConfigFileName)
	}
	return p
}
