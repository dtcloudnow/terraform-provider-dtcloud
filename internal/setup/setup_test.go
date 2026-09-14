package setup

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The file this command writes has to be the file the provider reads. These
// tests check the shape rather than round-tripping through the config package,
// which would be an import cycle.

func TestWriteSingleAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := write(path, "", "AK", "SK", "https://api.example/v1", "2"); err != nil {
		t.Fatalf("write: %s", err)
	}

	var doc struct {
		API struct {
			AccessKey string `yaml:"access_key"`
			SecretKey string `yaml:"secret_key"`
			BaseURL   string `yaml:"base_url"`
		} `yaml:"api"`
		RegionID string `yaml:"region_id"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("what was written is not valid YAML: %s\n%s", err, raw)
	}
	if doc.API.AccessKey != "AK" || doc.API.SecretKey != "SK" {
		t.Errorf("credentials wrong: %+v", doc)
	}
	if doc.API.BaseURL != "https://api.example/v1" || doc.RegionID != "2" {
		t.Errorf("endpoint or region wrong: %+v", doc)
	}
}

func TestWriteProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := write(path, "dev", "AK", "SK", "", "2"); err != nil {
		t.Fatalf("write: %s", err)
	}

	var doc struct {
		DefaultProfile string `yaml:"default_profile"`
		Profiles       map[string]struct {
			API struct {
				AccessKey string `yaml:"access_key"`
				BaseURL   string `yaml:"base_url"`
			} `yaml:"api"`
			RegionID string `yaml:"region_id"`
		} `yaml:"profiles"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not valid YAML: %s\n%s", err, raw)
	}
	if doc.DefaultProfile != "dev" {
		t.Errorf("default_profile = %q, want dev — otherwise the profile it just wrote is not the one used", doc.DefaultProfile)
	}
	p, ok := doc.Profiles["dev"]
	if !ok {
		t.Fatalf("no dev profile in:\n%s", raw)
	}
	if p.API.AccessKey != "AK" || p.RegionID != "2" {
		t.Errorf("profile contents wrong: %+v", p)
	}
	// An endpoint that was not given must not be written as an empty string —
	// the provider would then take "" as a deliberate choice rather than
	// falling through to the SDK default.
	if p.API.BaseURL != "" || strings.Contains(string(raw), "base_url") {
		t.Errorf("base_url should be absent when not supplied:\n%s", raw)
	}
}

// The file holds a secret key, so it goes out owner-readable and nothing else.
func TestWrittenFileIsLockedDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on Windows; os.Chmod sets the read-only attribute instead")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := write(path, "", "AK", "SK", "", "1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fs.FileMode(0o400) {
		t.Errorf("mode = %#o, want 0400", got)
	}
}

// Writing twice has to work. The first file is 0400, which cannot be truncated,
// so a naive rewrite fails with permission denied on the second run.
func TestWriteOverAnExistingReadOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := write(path, "", "first", "SK", "", "1"); err != nil {
		t.Fatal(err)
	}
	if err := write(path, "", "second", "SK", "", "1"); err != nil {
		t.Fatalf("second write failed — the 0400 file could not be replaced: %s", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "second") {
		t.Errorf("the file was not replaced:\n%s", raw)
	}
}

// Every value on the command line means no prompting at all, which is what
// makes the command usable from a script.
func TestRunNonInteractiveRefusesWithoutVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out, errOut bytes.Buffer

	// No -force, and the endpoint points nowhere, so verification must fail and
	// nothing may be written.
	code := Run([]string{
		"-access-key", "AK", "-secret-key", "SK", "-region-id", "1",
		"-api-url", "http://127.0.0.1:1/api/v1",
		"-config-file", path,
	}, strings.NewReader(""), &out, &errOut)

	if code == 0 {
		t.Error("expected a non-zero exit when the credentials do not verify")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("nothing should have been written when verification failed")
	}
	if !strings.Contains(errOut.String(), "Nothing was written") {
		t.Errorf("the failure should say nothing was written: %q", errOut.String())
	}
}

// -force saves regardless, which is what an offline or air-gapped setup needs.
func TestRunForceSavesWithoutVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out, errOut bytes.Buffer

	code := Run([]string{
		"-force",
		"-access-key", "AK", "-secret-key", "SK", "-region-id", "1",
		"-api-url", "http://127.0.0.1:1/api/v1",
		"-config-file", path,
	}, strings.NewReader(""), &out, &errOut)

	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("-force should have written the file anyway: %s", err)
	}
	if !strings.Contains(out.String(), "saving anyway") {
		t.Errorf("saving unverified credentials should be called out: %q", out.String())
	}
}

// A missing value is prompted for, and reading from a pipe has to work so the
// command can be driven by a script or a test.
func TestRunPromptsForWhatIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out, errOut bytes.Buffer

	code := Run([]string{"-force", "-api-url", "http://127.0.0.1:1/api/v1", "-config-file", path},
		strings.NewReader("prompted-key\nprompted-secret\n7\n"), &out, &errOut)

	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"prompted-key", "prompted-secret", "7"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%q not in the written file:\n%s", want, raw)
		}
	}
	for _, label := range []string{"API access key", "API secret key", "Region id"} {
		if !strings.Contains(out.String(), label) {
			t.Errorf("never prompted for %q: %q", label, out.String())
		}
	}
}

// Refusing to clobber is the difference between a mistyped re-run and a lost
// credential.
func TestRunRefusesToOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := write(path, "", "existing", "SK", "", "1"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer

	code := Run([]string{"-access-key", "AK", "-secret-key", "SK", "-region-id", "1", "-config-file", path},
		strings.NewReader(""), &out, &errOut)

	if code == 0 {
		t.Error("expected a non-zero exit rather than replacing the file")
	}
	if !strings.Contains(errOut.String(), "-force") {
		t.Errorf("the error should name the flag that would allow it: %q", errOut.String())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "existing") {
		t.Error("the existing file was modified")
	}
}
