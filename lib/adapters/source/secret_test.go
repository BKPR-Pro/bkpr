package source

import (
	"fmt"
	"strings"
	"testing"
)

// stubSecretCmd swaps the command runner for a fake that records how it was called and returns a
// canned secret, so the resolver is tested without a real secret store.
func stubSecretCmd(t *testing.T, out string, err error) *[]string {
	t.Helper()
	var argv []string
	prev := runSecretCmd
	runSecretCmd = func(name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		return []byte(out), err
	}
	t.Cleanup(func() { runSecretCmd = prev })
	return &argv
}

// A reference is resolved by running the configured command with the reference substituted for {},
// and the secret comes back trimmed of its trailing newline.
func TestResolveSecretRunsConfiguredCommand(t *testing.T) {
	argv := stubSecretCmd(t, "hunter2\n", nil)

	got, err := resolveSecret("op://Private/Acme/password", "op read {}")
	if err != nil {
		t.Fatalf("resolveSecret: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("secret = %q, want hunter2 (newline trimmed)", got)
	}
	if want := []string{"op", "read", "op://Private/Acme/password"}; strings.Join(*argv, " ") != strings.Join(want, " ") {
		t.Errorf("ran %v, want %v", *argv, want)
	}
}

// With no command configured the default is `op read <ref>`, so a 1Password reference works out of
// the box while any other store can be plugged by naming its command.
func TestResolveSecretDefaultsToOnePassword(t *testing.T) {
	argv := stubSecretCmd(t, "s3cret", nil)

	if _, err := resolveSecret("op://Private/Acme/username", ""); err != nil {
		t.Fatalf("resolveSecret: %v", err)
	}
	if want := []string{"op", "read", "op://Private/Acme/username"}; strings.Join(*argv, " ") != strings.Join(want, " ") {
		t.Errorf("ran %v, want the default %v", *argv, want)
	}
}

// A store that names the reference somewhere other than the end (Keychain) places it wherever {}
// appears rather than always appending it.
func TestResolveSecretSubstitutesPlaceholderInPlace(t *testing.T) {
	argv := stubSecretCmd(t, "kc-secret", nil)

	if _, err := resolveSecret("Acme/password", "security find-generic-password -s {} -w"); err != nil {
		t.Fatalf("resolveSecret: %v", err)
	}
	want := "security find-generic-password -s Acme/password -w"
	if strings.Join(*argv, " ") != want {
		t.Errorf("ran %q, want %q", strings.Join(*argv, " "), want)
	}
}

// A store that cannot produce the secret stops the caller loudly rather than yielding an empty
// credential that would silently fail the sign-in.
func TestResolveSecretPropagatesCommandFailure(t *testing.T) {
	stubSecretCmd(t, "", fmt.Errorf("op: item not found"))

	if _, err := resolveSecret("op://Private/Acme/password", "op read {}"); err == nil {
		t.Fatal("a failed resolver command should be an error, not an empty secret")
	}
}

// An empty reference resolves to an empty value without running anything, so optional fields (a
// second security answer, say) can be left unset.
func TestResolveSecretEmptyReferenceSkipsCommand(t *testing.T) {
	argv := stubSecretCmd(t, "should-not-run", nil)

	got, err := resolveSecret("", "op read {}")
	if err != nil || got != "" {
		t.Fatalf("empty ref = (%q, %v), want (\"\", nil)", got, err)
	}
	if len(*argv) != 0 {
		t.Errorf("resolver ran %v for an empty reference, want no command", *argv)
	}
}

// resolveCredentials turns a whole field->reference map into field->secret, so a connector's
// username, password, and security answers are fetched together.
func TestResolveCredentialsResolvesEveryField(t *testing.T) {
	prev := runSecretCmd
	runSecretCmd = func(name string, args ...string) ([]byte, error) {
		// Echo the reference (last arg) back as its secret, so each field is distinguishable.
		return []byte("secret-for:" + args[len(args)-1]), nil
	}
	t.Cleanup(func() { runSecretCmd = prev })

	got, err := resolveCredentials(map[string]string{
		"username": "op://V/Acme/username",
		"password": "op://V/Acme/password",
	}, "op read {}")
	if err != nil {
		t.Fatalf("resolveCredentials: %v", err)
	}
	if got["username"] != "secret-for:op://V/Acme/username" || got["password"] != "secret-for:op://V/Acme/password" {
		t.Errorf("resolved = %v", got)
	}
}
