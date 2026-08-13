package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// LogoutTimeout bounds the logout request. It runs on the teardown path, which a
// quit waits for, so a gateway that will not answer must not be able to hold the
// process open: give up quickly and let the gateway's own timeout clear the
// session as before.
const LogoutTimeout = 5 * time.Second

// Logout ends an SSL-VPN session on the gateway, given the SVPNCOOKIE that
// created it.
//
// openconnect does not do this for the Fortinet protocol — it has logout support
// for Juniper and GlobalProtect only — so closing the tunnel leaves the session
// ESTABLISHED on the FortiGate until the gateway times it out. On a gateway
// limited to one SSL-VPN session per user that makes every reconnect fail in the
// meantime: measured against a real gateway, five separate freshly minted cookies
// were refused over 3.5 minutes after a clean disconnect before one was accepted.
// FortiClient sends this request, which is why it reconnects immediately.
//
// gatewayURL is the base URL ("https://host:port"). The cookie is sent as
// SVPNCOOKIE, the same name the tunnel uses. Any error is returned for logging
// and is not fatal: failing to log out costs the delay this exists to avoid,
// nothing more.
func Logout(ctx context.Context, client *http.Client, gatewayURL, cookie string) error {
	if gatewayURL == "" || cookie == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/remote/logout", nil)
	if err != nil {
		return fmt.Errorf("auth: build logout request: %w", err)
	}
	// Set the header directly rather than through a jar: the value is opaque and
	// must reach the gateway byte-for-byte, and a jar would drop it on any host
	// mismatch after a redirect.
	req.Header.Set("Cookie", "SVPNCOOKIE="+cookie)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: logout request: %w", err)
	}
	defer resp.Body.Close()
	// Only a 2xx means the gateway accepted the logout.
	//
	// A redirect must NOT count as success, however tempting: this gateway answers
	// an unauthenticated /remote/logout with a 307 to the SAML IdP, and treating
	// that as "logged out" made the app log "session ended on the gateway" when
	// nothing had ended. A log line that lies is worse than no log line — it sent
	// this investigation down the wrong path for a while.
	if resp.StatusCode >= 300 {
		loc := resp.Header.Get("Location")
		if loc != "" {
			// Do not echo the Location back: it carries a SAMLRequest.
			return fmt.Errorf("auth: logout not accepted (HTTP %d, redirected to the identity provider — "+
				"the session was probably already gone from the gateway's point of view)", resp.StatusCode)
		}
		return fmt.Errorf("auth: logout not accepted (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// LogoutClient returns the http.Client Logout should use: a short timeout and no
// cookie jar, so nothing from this one-shot request is retained.
func LogoutClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: LogoutTimeout, Jar: jar}
}
