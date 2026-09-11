// Package setup implements `terraform-provider-dtcloud configure`, the one-off
// interactive command that writes the provider's configuration file.
//
// A provider cannot prompt: it is a plugin Terraform starts over gRPC, with no
// terminal and often nobody on the other end, so credentials have to be in place
// beforehand. Running this binary with an argument makes it a setup tool as well,
// so no second tool has to be installed. It is not the only way in — Terraform's
// own `variable` prompting and environment variables both work.
package setup

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"golang.org/x/term"
)

// Run executes the configure command. args are the arguments after the
// subcommand name. It returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		accessKey  = fs.String("access-key", "", "API access key. Prompted for when omitted.")
		secretKey  = fs.String("secret-key", "", "API secret key. Prompted for when omitted.")
		regionID   = fs.String("region-id", "", "Region id, sent as serverId on every call. Prompted for when omitted.")
		endpoint   = fs.String("api-url", "", "Base URL of the API. Optional; the SDK's default is used when empty.")
		profile    = fs.String("profile", "", "Write these settings as a named profile instead of the single-account form.")
		configFile = fs.String("config-file", "", "Where to write. Defaults to the provider's own configuration path.")
		force      = fs.Bool("force", false, "Overwrite an existing file, and save even if the credentials do not verify.")
	)
	fs.Usage = func() {
		fmt.Fprint(stderr, `terraform-provider-dtcloud configure

Writes the credentials the provider reads, so that a Terraform configuration can
say only:

    provider "dtcloud" {}

Run it with no flags to be prompted, or pass them all to script it.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := *configFile
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}

	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(stderr, "error: %s already exists. Pass -force to replace it.\n", path)
		return 1
	}

	in := bufio.NewReader(stdin)
	var err error

	if *accessKey == "" {
		if *accessKey, err = prompt(in, stdout, "API access key", false); err != nil {
			fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}
	if *secretKey == "" {
		if *secretKey, err = prompt(in, stdout, "API secret key", true); err != nil {
			fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}
	if *regionID == "" {
		if *regionID, err = prompt(in, stdout, "Region id", false); err != nil {
			fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}

	if *accessKey == "" || *secretKey == "" || *regionID == "" {
		fmt.Fprintln(stderr, "error: the access key, the secret key and the region id are all required.")
		return 1
	}

	// Check the credentials before writing them: a key with a stray space, or the
	// wrong region, is otherwise discovered much later and looks like a broken
	// provider rather than a typo.
	if verifyErr := verify(*accessKey, *secretKey, *endpoint, *regionID); verifyErr != nil {
		if !*force {
			fmt.Fprintf(stderr, "\nThose credentials were rejected: %s\n", verifyErr)
			fmt.Fprintf(stderr, "Nothing was written. Check them, or pass -force to save anyway.\n")
			return 1
		}
		fmt.Fprintf(stdout, "\nWarning: those credentials were rejected (%s), saving anyway because -force was given.\n", verifyErr)
	} else {
		fmt.Fprintln(stdout, "\nCredentials verified.")
	}

	if err := write(path, *profile, *accessKey, *secretKey, *endpoint, *regionID); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Written to %s (owner-readable only).\n\n", path)
	fmt.Fprint(stdout, "Your Terraform configuration now needs nothing but:\n\n    provider \"dtcloud\" {}\n\n")
	if *profile != "" {
		fmt.Fprintf(stdout, "Select this profile with:\n\n    provider \"dtcloud\" {\n      profile = %q\n    }\n\n", *profile)
	}
	return 0
}

// prompt reads one value, echoing a star per character when the value is secret
// and there is a terminal to do it on. Piped input falls through to a plain line
// read, so the command stays usable from a script.
func prompt(in *bufio.Reader, out io.Writer, label string, secret bool) (string, error) {
	fmt.Fprintf(out, "%s: ", label)

	if secret {
		if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
			return readMasked(fd, out)
		}
	}

	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readMasked reads a secret one character at a time, echoing a star for each:
// echoing nothing reads as a frozen program when a long key is pasted. It does
// leak the length of the secret, which for these keys is fixed and public.
func readMasked(fd int, out io.Writer) (string, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		// No raw mode available: fall back to no echo rather than to echoing
		// the secret in clear text.
		raw, readErr := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if readErr != nil {
			return "", readErr
		}
		return strings.TrimSpace(string(raw)), nil
	}
	defer func() { _ = term.Restore(fd, state) }()

	var value []rune
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		switch c := buf[0]; c {
		case '\r', '\n':
			fmt.Fprint(out, "\r\n")
			return strings.TrimSpace(string(value)), nil

		case 3: // Ctrl-C: raw mode swallows the interrupt, so honour it here.
			fmt.Fprint(out, "\r\n")
			return "", errors.New("cancelled")

		case 8, 127: // backspace and delete
			if len(value) > 0 {
				value = value[:len(value)-1]
				// Move back over the star, overwrite it, move back again.
				fmt.Fprint(out, "\b \b")
			}

		default:
			// Ignore the rest of the control range, so a stray escape sequence from
			// an arrow key does not become part of the key.
			if c < 32 {
				continue
			}
			value = append(value, rune(c))
			fmt.Fprint(out, "*")
		}
	}
	fmt.Fprint(out, "\r\n")
	return strings.TrimSpace(string(value)), nil
}

// verify makes one cheap authenticated call. The route it uses needs the key
// pair and the region, so a failure means one of the three is wrong.
func verify(accessKey, secretKey, endpoint, regionID string) error {
	opts := []dtgo.ClientOpt{dtgo.SetApiKey(accessKey, secretKey)}
	if endpoint != "" {
		opts = append(opts, dtgo.SetBaseURL(endpoint))
	}
	client, err := dtgo.New(http.DefaultClient, opts...)
	if err != nil {
		return err
	}
	client.ServerId = regionID

	_, _, err = client.SecurityGroup.ListSecurityGroups(context.Background(), nil)
	return err
}

// write puts the file in place, owner-readable and nothing else: created 0600
// and then tightened to 0400, which cannot be written to even by its creator.
func write(path, profile, accessKey, secretKey, endpoint, regionID string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}
	// An existing file may be 0400 from a previous run and cannot be truncated;
	// remove it rather than fight the permissions.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not replace %s: %w", path, err)
	}

	var b strings.Builder
	b.WriteString("# Written by `terraform-provider-dtcloud configure`.\n")
	b.WriteString("# Holds an API secret key: keep it readable by you and nobody else.\n")

	indent := ""
	if profile != "" {
		fmt.Fprintf(&b, "\ndefault_profile: %s\n\nprofiles:\n  %s:\n", profile, profile)
		indent = "    "
	}
	fmt.Fprintf(&b, "%sapi:\n", indent)
	fmt.Fprintf(&b, "%s  access_key: %q\n", indent, accessKey)
	fmt.Fprintf(&b, "%s  secret_key: %q\n", indent, secretKey)
	if endpoint != "" {
		fmt.Fprintf(&b, "%s  base_url: %q\n", indent, endpoint)
	}
	fmt.Fprintf(&b, "%sregion_id: %q\n", indent, regionID)

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return fmt.Errorf("wrote %s but could not make it read-only: %w", path, err)
	}
	return nil
}
