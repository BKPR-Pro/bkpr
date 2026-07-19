package source

import (
	"fmt"
	"os/exec"
	"strings"
)

// Credentials for an unattended bank sign-in never live in the book of record -- the event log is
// committed to git -- nor anywhere on disk. A connector stores only a *reference* to each secret
// (an "op://Private/RBC/password", a Keychain service name), which names where the secret lives, not
// the secret. At sign-in the reference is handed to a resolver command that prints the secret, and
// the value exists only in memory for the length of the run.
//
// The resolver command is what keeps this generic: bkpr knows nothing about 1Password. The
// default command happens to be `op read`, so a 1Password reference works out of the box, but naming
// a different command plugs in macOS Keychain (`security find-generic-password -s {} -w`), pass, or
// any CLI that prints a secret to stdout. `{}` in the command is replaced by the reference; a command
// with no `{}` gets the reference appended as its final argument.

// defaultSecretCmd resolves a 1Password secret reference, the common case for this tool's author.
const defaultSecretCmd = "op read {}"

// runSecretCmd runs the resolver and returns its stdout. It is a package variable so tests can supply
// a secret without a real store.
var runSecretCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// resolveSecret turns one reference into its secret by running the resolver command. An empty
// reference resolves to an empty value without running anything, so optional fields can be left
// unset. The secret is returned trimmed of a trailing newline, which CLIs like `op` and `security`
// print.
func resolveSecret(ref, cmdTemplate string) (string, error) {
	if ref == "" {
		return "", nil
	}
	tmpl := strings.TrimSpace(cmdTemplate)
	if tmpl == "" {
		tmpl = defaultSecretCmd
	}
	argv := strings.Fields(tmpl)
	if len(argv) == 0 {
		return "", fmt.Errorf("import: empty secret-resolver command")
	}

	substituted := false
	for i, a := range argv {
		if strings.Contains(a, "{}") {
			argv[i] = strings.ReplaceAll(a, "{}", ref)
			substituted = true
		}
	}
	if !substituted {
		argv = append(argv, ref)
	}

	out, err := runSecretCmd(argv[0], argv[1:]...)
	if err != nil {
		return "", fmt.Errorf("import: resolving secret %q with %q: %w", ref, argv[0], err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// resolveCredentials resolves a whole field->reference map into field->secret, so a connector's
// username, password, and security answers are fetched together. Any one failing stops the lot, so a
// half-resolved credential never reaches the browser.
func resolveCredentials(refs map[string]string, cmdTemplate string) (map[string]string, error) {
	out := make(map[string]string, len(refs))
	for field, ref := range refs {
		v, err := resolveSecret(ref, cmdTemplate)
		if err != nil {
			return nil, err
		}
		out[field] = v
	}
	return out, nil
}
