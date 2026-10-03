package authkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"

	"latere.ai/x/pkg/oidc"
)

// DeviceCodeClient drives the RFC 8628 device-authorization flow end to end
// for CLI and desktop callers:
//
//  1. Initiates a device-code grant against the auth service.
//  2. Renders the user-code and verification URI to Output (default: stderr).
//  3. Best-effort opens the verification URI in the host's default browser.
//  4. Polls the token endpoint until approval, denial, or expiry.
//  5. Persists the resulting *oauth2.Token via Store on success.
//
// All interactive surfaces (Output, OpenBrowser) are exposed as overridable
// fields so tests can drive Login deterministically without forking a real
// browser.
type DeviceCodeClient struct {
	// OIDC is the underlying client used for DeviceAuth / DeviceAccessToken.
	OIDC *oidc.Client

	// Store receives the *oauth2.Token after a successful flow.
	Store TokenStore

	// Output is where the user code and verification URI are printed.
	// Defaults to os.Stderr so stdout stays usable for piped output.
	Output io.Writer

	// OpenBrowser is called once with the verification URI complete (or
	// the bare URI if no complete form is returned). Errors are ignored
	// so a missing browser, headless CI, or denied permission does not
	// block the printed-URI fallback. Defaults to openBrowser, the
	// runtime.GOOS-based opener below.
	OpenBrowser func(url string) error

	// ExtraParams carries auth-server extension parameters forwarded on
	// DeviceAuth. auth.latere.ai honours `org_id` here to scope the
	// resulting token.
	ExtraParams url.Values
}

// NewDeviceCodeClient wires a DeviceCodeClient over c and store. Either
// argument may be nil; nil OIDC causes Login to fail loudly, and nil Store
// causes Login to return the issued token but not persist it.
func NewDeviceCodeClient(c *oidc.Client, store TokenStore) *DeviceCodeClient {
	return &DeviceCodeClient{OIDC: c, Store: store}
}

// Login runs the full device-authorization flow synchronously. Returns once
// the issued token is persisted (or ctx is cancelled / the flow expires).
//
// The function is safe to call against a partially-configured client: if
// OIDC is nil it returns an error; if Store is nil the token is still
// fetched but no persistence occurs, so callers can capture it from a
// custom OpenBrowser or test hook.
func (d *DeviceCodeClient) Login(ctx context.Context) error {
	if d == nil || d.OIDC == nil {
		return errors.New("authkit: DeviceCodeClient.OIDC is nil")
	}
	da, err := d.OIDC.DeviceAuth(ctx, d.ExtraParams)
	if err != nil {
		return fmt.Errorf("authkit: device authorization: %w", err)
	}

	out := d.Output
	if out == nil {
		out = os.Stderr
	}
	uri := da.VerificationURIComplete
	if uri == "" {
		uri = da.VerificationURI
	}
	// When the provider returns a verification_uri_complete, the user code is
	// already encoded in the link (and the CLI tries to open it in a browser),
	// so asking the user to "enter the code" too is redundant and confusing.
	// Only the bare-URI fallback needs manual code entry.
	if da.VerificationURIComplete != "" {
		_, _ = fmt.Fprintf(out, "\nTo sign in, open this link (code %s is already filled in):\n\n  %s\n\nWaiting for you to approve in the browser...\n", da.UserCode, da.VerificationURIComplete)
	} else {
		_, _ = fmt.Fprintf(out, "\nTo sign in, visit:\n\n  %s\n\nand enter this code:\n\n  %s\n\nWaiting for you to approve...\n", da.VerificationURI, da.UserCode)
	}

	openFn := d.OpenBrowser
	if openFn == nil {
		openFn = openBrowser
	}
	// Best-effort. Errors are intentionally swallowed: the printed URI
	// above is always the primary UX.
	_ = openFn(uri)

	tok, err := d.OIDC.DeviceAccessToken(ctx, da)
	if err != nil {
		return fmt.Errorf("authkit: device access token: %w", err)
	}
	if d.Store == nil {
		return nil
	}
	if err := d.Store.Save(tok); err != nil {
		return fmt.Errorf("authkit: persist token: %w", err)
	}
	return nil
}

// browserOpener is the package-level hook execCommand uses so tests can
// inject a fake without spawning a real browser. Tests reassign this in
// t.Cleanup.
var execCommand = exec.Command

func openBrowser(url string) error {
	if url == "" {
		return errors.New("authkit: empty URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = execCommand("open", url)
	case "windows":
		cmd = execCommand("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, freebsd, openbsd, netbsd, ...
		cmd = execCommand("xdg-open", url)
	}
	return cmd.Start()
}
