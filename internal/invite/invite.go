// Package invite triggers the password-reset mail that lets a freshly created
// user set a first password. The operator never sets passwords itself.
package invite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Mode selects who sends the invitation mail.
const (
	ModeNone     = "none"
	ModeLLDAP    = "lldap"
	ModeAuthelia = "authelia"
)

// Inviter asks an external system to mail username a link to set a password.
type Inviter interface {
	Invite(ctx context.Context, username string) error
}

// New returns the Inviter for mode, or nil for ModeNone. lldapURL is the base
// URL of lldap's web port, autheliaURL the public URL of the Authelia portal.
// httpClient may be nil.
func New(mode, lldapURL, autheliaURL string, httpClient *http.Client) (Inviter, error) {
	switch mode {
	case ModeNone:
		return nil, nil
	case ModeLLDAP:
		return NewLLDAP(lldapURL, httpClient), nil
	case ModeAuthelia:
		if autheliaURL == "" {
			return nil, errors.New("--authelia-url is required with --invite=authelia")
		}
		u, err := url.Parse(autheliaURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("--authelia-url %q must be an absolute http(s) URL", autheliaURL)
		}
		return NewAuthelia(autheliaURL, httpClient), nil
	}
	return nil, fmt.Errorf("--invite=%q is invalid, want %s, %s or %s", mode, ModeNone, ModeLLDAP, ModeAuthelia)
}

func defaultClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// snippet returns the start of a response body for error messages.
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 256))
	return strings.TrimSpace(string(b))
}
