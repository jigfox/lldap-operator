//go:build integration

// Package lldaptest starts a real lldap in a container for integration tests.
package lldaptest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
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

// Start runs lldap and registers its cleanup with t.
func Start(ctx context.Context, t *testing.T) *Server {
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
	return &Server{URL: fmt.Sprintf("http://%s:%s", host, port.Port()), Username: "admin", Password: password}
}
