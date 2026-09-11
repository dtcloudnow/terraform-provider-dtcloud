package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeConfig puts a file in a temp directory and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %s", err)
	}
	return path
}

// isolateConfigHome points os.UserConfigDir at a temp directory, so a test can
// exercise the default path without reading the developer's real credentials.
func isolateConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("APPDATA", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	return home
}

const profilesConfig = `
default_profile: dev

profiles:
  dev:
    api:
      access_key: dev-access
      secret_key: dev-secret
      base_url: https://dev.cms.dt.net.tr/api/v1
    region_id: 2
  prod:
    api:
      access_key: prod-access
      secret_key: prod-secret
    region_id: "1"
`

// The shape dtctl itself writes: no profiles, and region_id as a bare integer,
// which a string field would fail the whole decode on.
const dtctlShapedConfig = `
api:
    access_key: cli-access
    base_url: https://dev.cms.dt.net.tr/api/v1
    secret_key: cli-secret
output: text
region_id: 1
`

func TestProfileSelection(t *testing.T) {
	path := writeConfig(t, profilesConfig)

	for _, tc := range []struct {
		name, profile, wantKey, wantRegion string
	}{
		{"default_profile is used when none is named", "", "dev-access", "2"},
		{"an explicit profile wins", "prod", "prod-access", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, err := Load(path, tc.profile)
			if err != nil {
				t.Fatalf("Load: %s", err)
			}
			if got.AccessKey != tc.wantKey {
				t.Errorf("access key = %q, want %q", got.AccessKey, tc.wantKey)
			}
			if got.RegionID != tc.wantRegion {
				t.Errorf("region = %q, want %q", got.RegionID, tc.wantRegion)
			}
		})
	}
}

// A dtctl config.yaml is usable as-is; the bare `region_id: 1` is the part that
// would otherwise break.
func TestDtctlShapedConfigIsAccepted(t *testing.T) {
	got, _, _, err := Load(writeConfig(t, dtctlShapedConfig), "")
	if err != nil {
		t.Fatalf("Load: %s", err)
	}
	if got.AccessKey != "cli-access" || got.SecretKey != "cli-secret" {
		t.Errorf("credentials not read: %+v", got)
	}
	if got.RegionID != "1" {
		t.Errorf("region = %q, want \"1\" — a bare YAML integer has to be accepted", got.RegionID)
	}
	if got.APIEndpoint != "https://dev.cms.dt.net.tr/api/v1" {
		t.Errorf("endpoint = %q", got.APIEndpoint)
	}
}

func TestUnknownProfileNamesTheOnesThatExist(t *testing.T) {
	_, _, _, err := Load(writeConfig(t, profilesConfig), "staging")
	if err == nil {
		t.Fatal("expected an error for a profile that is not in the file")
	}
	// The message has to be actionable: someone who mistyped needs to see what
	// they could have typed.
	for _, want := range []string{"staging", "dev", "prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %s", want, err)
		}
	}
}

func TestNamingAProfileInAFileThatHasNoneIsAnError(t *testing.T) {
	_, _, _, err := Load(writeConfig(t, dtctlShapedConfig), "prod")
	if err == nil {
		t.Fatal("expected an error: the file has no profiles block")
	}
	if !strings.Contains(err.Error(), "no `profiles` block") {
		t.Errorf("unhelpful error: %s", err)
	}
}

// A missing file is only a problem when someone asked for that file by name. At
// the default path it means the machine configures the provider some other way.
func TestMissingFile(t *testing.T) {
	t.Run("at the default path it is not an error", func(t *testing.T) {
		isolateConfigHome(t)
		got, path, _, err := Load("", "")
		if err != nil {
			t.Fatalf("Load: %s", err)
		}
		if got != (FileValues{}) {
			t.Errorf("expected nothing, got %+v", got)
		}
		if !strings.Contains(path, ConfigDirName) {
			t.Errorf("resolved path %q should sit under %q", path, ConfigDirName)
		}
	})

	t.Run("an explicitly named one is", func(t *testing.T) {
		_, _, _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), "")
		if err == nil {
			t.Fatal("expected an error for a named file that does not exist")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("unhelpful error: %s", err)
		}
	})
}

func TestMalformedYAMLIsReportedWithTheFileName(t *testing.T) {
	path := writeConfig(t, "profiles:\n  dev:\n   api:\n  bad indent: [\n")
	_, _, _, err := Load(path, "")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the file: %s", err)
	}
}

// The file holds an API secret key, so it is treated the way an SSH private key
// is. At the default path the provider tightens it rather than only complaining.
func TestPermissionsAreTightenedAtTheDefaultPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on Windows; os.Chmod maps 0400 onto the read-only attribute instead")
	}

	home := isolateConfigHome(t)
	dir := filepath.Join(home, ConfigDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(dtctlShapedConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, warnings, err := Load("", "")
	if err != nil {
		t.Fatalf("Load: %s", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fs.FileMode(0o400) {
		t.Errorf("mode = %#o, want 0400 — a world-readable credentials file must be tightened", got)
	}
	if len(warnings) == 0 {
		t.Error("tightening a world-readable file should be reported, not done silently")
	}
}

// A file named explicitly is warned about but left alone: it may be dtctl's own
// config, and making that read-only would break the CLI to tidy up the provider.
func TestAnExplicitlyNamedFileIsWarnedAboutButNotChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on Windows")
	}

	path := writeConfig(t, dtctlShapedConfig)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, warnings, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load: %s", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fs.FileMode(0o644) {
		t.Errorf("mode = %#o, want it left at 0644", got)
	}
	if len(warnings) == 0 {
		t.Error("a world-readable credentials file should still be warned about")
	}
}

func TestScalarToString(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"2", "2"},
		{"  2 ", "2"},
		{2, "2"},
		{int64(2), "2"},
		{float64(2), "2"},
	} {
		if got := scalarToString(tc.in); got != tc.want {
			t.Errorf("scalarToString(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The permission rule itself, tested on every platform; only the chmod behind
// it is POSIX-specific.
func TestPermissionAction(t *testing.T) {
	for _, tc := range []struct {
		mode        fs.FileMode
		explicit    bool
		wantTighten bool
		wantTooOpen bool
		why         string
	}{
		{0o400, false, false, false, "already exactly right, leave it alone"},
		{0o400, true, false, false, "same, however it was named"},
		{0o600, false, true, false, "owner-writable at our own path: tighten, but nobody else could read it"},
		{0o600, true, false, false, "owner-writable elsewhere: not our file to change, and not a leak"},
		{0o644, false, true, true, "world-readable at our own path: tighten and say so"},
		{0o644, true, false, true, "world-readable elsewhere: warn, but do not break the other tool"},
		{0o640, false, true, true, "group-readable counts as readable by others"},
		{0o000, false, false, false, "tighter than we ask for is fine"},
	} {
		tighten, tooOpen := permissionAction(tc.mode, tc.explicit)
		if tighten != tc.wantTighten || tooOpen != tc.wantTooOpen {
			t.Errorf("mode %#o explicit=%v: got (tighten=%v, tooOpen=%v), want (%v, %v) — %s",
				tc.mode, tc.explicit, tighten, tooOpen, tc.wantTighten, tc.wantTooOpen, tc.why)
		}
	}
}
