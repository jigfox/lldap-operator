package invite

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type seen struct {
	method, path, body string
	header             http.Header
}

func serve(t *testing.T, status int, reply string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.body, got.header = r.Method, r.URL.EscapedPath(), string(b), r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestLLDAP(t *testing.T) {
	srv, got := serve(t, http.StatusOK, "")
	if err := NewLLDAP(srv.URL+"/", nil).Invite(context.Background(), "al ice"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/auth/reset/step1/al%20ice" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if got.body != "" || got.header.Get("Authorization") != "" {
		t.Fatalf("body %q, authorization %q: the endpoint is unauthenticated", got.body, got.header.Get("Authorization"))
	}
}

func TestLLDAPErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		reply  string
	}{
		{http.StatusNotFound, ""}, // password reset disabled
		{http.StatusInternalServerError, "Could not send email: smtp down"},
	} {
		srv, _ := serve(t, tc.status, tc.reply)
		err := NewLLDAP(srv.URL, nil).Invite(context.Background(), "alice")
		if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(tc.status)) ||
			!strings.Contains(err.Error(), tc.reply) {
			t.Fatalf("status %d: err = %v", tc.status, err)
		}
	}
	srv, _ := serve(t, 200, "")
	srv.Close()
	if err := NewLLDAP(srv.URL, nil).Invite(context.Background(), "alice"); err == nil {
		t.Fatal("connection error not reported")
	}
}

func TestAuthelia(t *testing.T) {
	srv, got := serve(t, http.StatusOK, `{"status":"OK"}`)
	if err := NewAuthelia(srv.URL+"/", nil).Invite(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/reset-password/identity/start" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got.body), &body); err != nil || len(body) != 1 || body["username"] != "alice" {
		t.Fatalf("body = %q (%v)", got.body, err)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	// The mail link is built from these, so they must name the portal itself.
	host := strings.TrimPrefix(srv.URL, "http://")
	if got.header.Get("X-Forwarded-Proto") != "http" || got.header.Get("X-Forwarded-Host") != host {
		t.Fatalf("forwarded headers = %q %q", got.header.Get("X-Forwarded-Proto"), got.header.Get("X-Forwarded-Host"))
	}
}

func TestAutheliaErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		reply  string
		want   string
	}{
		"mail failed":  {http.StatusOK, `{"status":"KO","message":"Operation failed."}`, "Operation failed."},
		"rate limited": {http.StatusTooManyRequests, "slow down", "HTTP 429"},
		"not json":     {http.StatusOK, "<html>", "unreadable response"},
		"disabled":     {http.StatusNotFound, "", "HTTP 404"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := serve(t, tc.status, tc.reply)
			err := NewAuthelia(srv.URL, nil).Invite(context.Background(), "alice")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		mode, authelia string
		wantNil, fail  bool
	}{
		{mode: "none", wantNil: true},
		{mode: "lldap"},
		{mode: "authelia", authelia: "https://sso.example.com"},
		{mode: "authelia", fail: true},
		{mode: "authelia", authelia: "sso.example.com", fail: true},
		{mode: "smtp", fail: true},
		{mode: "", fail: true},
	} {
		inv, err := New(tc.mode, "http://lldap:17170", tc.authelia, nil)
		if (err != nil) != tc.fail || (err == nil && (inv == nil) != tc.wantNil) {
			t.Errorf("New(%q, %q) = %v, %v", tc.mode, tc.authelia, inv, err)
		}
	}
}
