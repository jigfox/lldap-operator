package invite

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// LLDAP uses lldap's own password-reset flow. It needs SMTP and the password
// reset enabled in lldap (the endpoint answers 404 otherwise); the mail links
// to lldap's own UI.
type LLDAP struct {
	baseURL string
	http    *http.Client
}

// NewLLDAP returns an Inviter for the lldap web port at baseURL.
func NewLLDAP(baseURL string, httpClient *http.Client) *LLDAP {
	return &LLDAP{baseURL: strings.TrimRight(baseURL, "/"), http: defaultClient(httpClient)}
}

// Invite calls the unauthenticated POST /auth/reset/step1/<user>
// (server/src/auth_service.rs). lldap answers 200 for unknown users, 500 when
// the mail could not be sent and 404 when password reset is disabled.
func (l *LLDAP) Invite(ctx context.Context, username string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"/auth/reset/step1/"+url.PathEscape(username), nil)
	if err != nil {
		return err
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("lldap password reset: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lldap password reset: HTTP %d: %s", resp.StatusCode, snippet(resp.Body))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
