package invite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Authelia starts Authelia's "Reset password?" flow, which mails the user
// Authelia's reset-password template with a link to the portal.
type Authelia struct {
	baseURL string
	scheme  string
	host    string
	http    *http.Client
}

// NewAuthelia returns an Inviter for the portal at baseURL (its public URL,
// e.g. https://sso.example.com). The mail's link is built from the
// X-Forwarded-* headers, so the URL decides where it points.
func NewAuthelia(baseURL string, httpClient *http.Client) *Authelia {
	a := &Authelia{baseURL: strings.TrimRight(baseURL, "/"), http: defaultClient(httpClient)}
	if u, err := url.Parse(a.baseURL); err == nil {
		a.scheme, a.host = u.Scheme, u.Host
	}
	return a
}

// Invite calls the unauthenticated POST /api/reset-password/identity/start
// (internal/handlers/handler_reset_password.go). Authelia answers 200 for
// unknown users and users without an email address, to prevent enumeration,
// so only a KO status in the body (mail could not be sent) is detectable.
// The link in the mail is built from X-Forwarded-Proto and X-Forwarded-Host,
// which are set explicitly so that it also points at the public URL when the
// request goes straight to the pod.
func (a *Authelia) Invite(ctx context.Context, username string) error {
	body, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/api/reset-password/identity/start", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", a.scheme)
	req.Header.Set("X-Forwarded-Host", a.host)
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("authelia password reset: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authelia password reset: HTTP %d: %s", resp.StatusCode, snippet(resp.Body))
	}
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("authelia password reset: unreadable response: %w", err)
	}
	if out.Status != "OK" {
		return fmt.Errorf("authelia password reset: status %q: %s", out.Status, out.Message)
	}
	return nil
}
