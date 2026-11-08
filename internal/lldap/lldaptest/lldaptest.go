//go:build integration

// Package lldaptest starts a real lldap in a container for integration tests.
package lldaptest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DefaultImage is the lldap image the integration tests run against;
// override it with LLDAP_IMAGE.
const DefaultImage = "lldap/lldap:v0.6.2"

// Server is a running lldap.
type Server struct {
	URL      string
	Username string
	Password string
}

// MailpitImage is the throwaway SMTP server for StartWithMail.
const MailpitImage = "axllent/mailpit:v1.21"

// Mailpit is a running SMTP catch-all.
type Mailpit struct{ apiURL string }

// Messages returns the recipients of every mail received so far.
func (m *Mailpit) Messages(t *testing.T) (recipients []string) {
	t.Helper()
	resp, err := http.Get(m.apiURL + "/api/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Messages []struct{ To []struct{ Address string } }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, msg := range out.Messages {
		for _, to := range msg.To {
			recipients = append(recipients, to.Address)
		}
	}
	return recipients
}

// Start runs lldap and registers its cleanup with t.
func Start(ctx context.Context, t *testing.T) *Server {
	t.Helper()
	srv, _ := start(ctx, t, false)
	return srv
}

// StartWithMail runs lldap with password reset enabled and its SMTP pointed at
// a mailpit container.
func StartWithMail(ctx context.Context, t *testing.T) (*Server, *Mailpit) {
	t.Helper()
	return start(ctx, t, true)
}

func start(ctx context.Context, t *testing.T, mail bool) (*Server, *Mailpit) {
	t.Helper()
	image := os.Getenv("LLDAP_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	const password = "integration-test-password"
	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"17170/tcp"},
		Env: map[string]string{
			"LLDAP_JWT_SECRET":     "integration-test-jwt-secret",
			"LLDAP_KEY_SEED":       "integration-test-key-seed",
			"LLDAP_LDAP_USER_PASS": password,
			"LLDAP_LDAP_BASE_DN":   "dc=example,dc=com",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("17170/tcp").WithStartupTimeout(2 * time.Minute),
	}
	var mp *Mailpit
	if mail {
		net, err := network.New(ctx)
		if err != nil {
			t.Fatalf("creating network: %v", err)
		}
		t.Cleanup(func() { _ = net.Remove(ctx) })
		mailReq := testcontainers.ContainerRequest{
			Image:          MailpitImage,
			ExposedPorts:   []string{"8025/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {"mailpit"}},
			WaitingFor:     wait.ForHTTP("/livez").WithPort("8025/tcp").WithStartupTimeout(2 * time.Minute),
		}
		mctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: mailReq, Started: true,
		})
		if err != nil {
			t.Fatalf("starting mailpit: %v", err)
		}
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(mctr) })
		host, err := mctr.Host(ctx)
		if err != nil {
			t.Fatal(err)
		}
		port, err := mctr.MappedPort(ctx, "8025/tcp")
		if err != nil {
			t.Fatal(err)
		}
		mp = &Mailpit{apiURL: fmt.Sprintf("http://%s:%s", host, port.Port())}
		req.Networks = []string{net.Name}
		req.Env["LLDAP_HTTP_URL"] = "http://lldap.test"
		req.Env["LLDAP_SMTP_OPTIONS__ENABLE_PASSWORD_RESET"] = "true"
		req.Env["LLDAP_SMTP_OPTIONS__SERVER"] = "mailpit"
		req.Env["LLDAP_SMTP_OPTIONS__PORT"] = "1025"
		req.Env["LLDAP_SMTP_OPTIONS__SMTP_ENCRYPTION"] = "NONE"
		req.Env["LLDAP_SMTP_OPTIONS__FROM"] = "LLDAP <lldap@example.com>"
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("starting lldap: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "17170/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{URL: fmt.Sprintf("http://%s:%s", host, port.Port()), Username: "admin", Password: password}, mp
}
