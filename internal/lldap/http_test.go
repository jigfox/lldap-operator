package lldap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// jwt builds an unsigned token with the given expiry.
func jwt(exp time.Time) string {
	payload, _ := json.Marshal(map[string]int64{"exp": exp.Unix()})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

type gqlCall struct {
	Query     string
	Variables map[string]any
}

type server struct {
	t      *testing.T
	mu     sync.Mutex
	logins int
	token  string // the token the server currently accepts
	calls  []gqlCall
	reply  string // data (or full body if it starts with `{"errors"`) for graphql
	ttl    time.Duration
}

func newServer(t *testing.T) (*server, *HTTPClient) {
	s := &server{t: t, ttl: time.Hour, reply: `{"data":{}}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/simple/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["username"] != "svc" || body["password"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.logins++
		s.token = jwt(time.Now().Add(s.ttl)) + fmt.Sprint(s.logins)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": s.token, "refreshToken": "r"})
	})
	mux.HandleFunc("/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var c gqlCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		s.calls = append(s.calls, c)
		_, _ = w.Write([]byte(s.reply))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, NewHTTPClient(srv.URL+"/", "svc", "secret", nil)
}

func (s *server) last() gqlCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func TestLoginAndReuseToken(t *testing.T) {
	s, c := newServer(t)
	s.reply = `{"data":{"groups":[{"id":1,"displayName":"lldap_admin"},{"id":7,"displayName":"family"}]}}`
	for range 2 {
		groups, err := c.ListGroups(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 2 || groups[1] != (Group{ID: 7, DisplayName: "family"}) {
			t.Fatalf("groups = %+v", groups)
		}
	}
	if s.logins != 1 {
		t.Fatalf("logins = %d, want 1", s.logins)
	}
}

func TestLoginFailure(t *testing.T) {
	_, c := newServer(t)
	c.password = "wrong"
	if _, err := c.ListGroups(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestRelogin(t *testing.T) {
	t.Run("on 401", func(t *testing.T) {
		s, c := newServer(t)
		if err := c.DeleteUser(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.token = "revoked"
		s.mu.Unlock()
		if err := c.DeleteUser(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
		if s.logins != 2 {
			t.Fatalf("logins = %d, want 2", s.logins)
		}
	})
	t.Run("before expiry", func(t *testing.T) {
		s, c := newServer(t)
		s.ttl = 10 * time.Second // inside the skew
		for range 2 {
			if err := c.DeleteUser(context.Background(), "a"); err != nil {
				t.Fatal(err)
			}
		}
		if s.logins != 2 {
			t.Fatalf("logins = %d, want 2", s.logins)
		}
	})
}

func TestGetUser(t *testing.T) {
	s, c := newServer(t)
	s.reply = `{"data":{"user":{"id":"alice","email":"a@example.com","displayName":"Alice","firstName":"A","lastName":"B","groups":[{"id":3,"displayName":"family"}]}}}`
	u, err := c.GetUser(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "alice" || u.Email != "a@example.com" || u.FirstName != "A" || len(u.Groups) != 1 || u.Groups[0].ID != 3 {
		t.Fatalf("user = %+v", u)
	}
	if got := s.last().Variables["id"]; got != "alice" {
		t.Fatalf("variables = %v", s.last().Variables)
	}
}

func TestGetUserNotFound(t *testing.T) {
	s, c := newServer(t)
	s.reply = `{"data":null,"errors":[{"message":"Entity not found: `+"`"+`bob`+"`"+`"}]}`
	_, err := c.GetUser(context.Background(), "bob")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGraphQLError(t *testing.T) {
	s, c := newServer(t)
	s.reply = `{"data":null,"errors":[{"message":"Permission denied"}]}`
	err := c.DeleteUser(context.Background(), "x")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestMutationShapes(t *testing.T) {
	email, name := "e@example.com", "N"
	tests := []struct {
		name  string
		run   func(c *HTTPClient) error
		reply string
		query string // substring of the query
		vars  string // JSON of the variables
	}{
		{
			"createUser",
			func(c *HTTPClient) error {
				return c.CreateUser(context.Background(), CreateUserInput{ID: "a", Email: "a@example.com", FirstName: "F"})
			},
			`{"data":{"createUser":{"id":"a"}}}`,
			"createUser(user: $user)",
			`{"user":{"email":"a@example.com","firstName":"F","id":"a"}}`,
		},
		{
			"updateUser",
			func(c *HTTPClient) error {
				return c.UpdateUser(context.Background(), UpdateUserInput{ID: "a", Email: &email, DisplayName: &name})
			},
			`{"data":{"updateUser":{"ok":true}}}`,
			"updateUser(user: $user)",
			`{"user":{"displayName":"N","email":"e@example.com","id":"a"}}`,
		},
		{
			"deleteUser",
			func(c *HTTPClient) error { return c.DeleteUser(context.Background(), "a") },
			`{"data":{"deleteUser":{"ok":true}}}`,
			"deleteUser(userId: $id)",
			`{"id":"a"}`,
		},
		{
			"updateGroup",
			func(c *HTTPClient) error { return c.UpdateGroup(context.Background(), 4, "new") },
			`{"data":{"updateGroup":{"ok":true}}}`,
			"updateGroup(group: $group)",
			`{"group":{"displayName":"new","id":4}}`,
		},
		{
			"deleteGroup",
			func(c *HTTPClient) error { return c.DeleteGroup(context.Background(), 4) },
			`{"data":{"deleteGroup":{"ok":true}}}`,
			"deleteGroup(groupId: $id)",
			`{"id":4}`,
		},
		{
			"addUserToGroup",
			func(c *HTTPClient) error { return c.AddUserToGroup(context.Background(), "a", 4) },
			`{"data":{"addUserToGroup":{"ok":true}}}`,
			"addUserToGroup(userId: $user, groupId: $group)",
			`{"group":4,"user":"a"}`,
		},
		{
			"removeUserFromGroup",
			func(c *HTTPClient) error { return c.RemoveUserFromGroup(context.Background(), "a", 4) },
			`{"data":{"removeUserFromGroup":{"ok":true}}}`,
			"removeUserFromGroup(userId: $user, groupId: $group)",
			`{"group":4,"user":"a"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := newServer(t)
			s.reply = tt.reply
			if err := tt.run(c); err != nil {
				t.Fatal(err)
			}
			call := s.last()
			if !strings.Contains(call.Query, tt.query) {
				t.Errorf("query %q lacks %q", call.Query, tt.query)
			}
			got, _ := json.Marshal(call.Variables)
			if string(got) != tt.vars {
				t.Errorf("variables = %s, want %s", got, tt.vars)
			}
		})
	}
}

func TestCreateGroup(t *testing.T) {
	s, c := newServer(t)
	s.reply = `{"data":{"createGroup":{"id":9,"displayName":"family"}}}`
	g, err := c.CreateGroup(context.Background(), "family")
	if err != nil {
		t.Fatal(err)
	}
	if g != (Group{ID: 9, DisplayName: "family"}) {
		t.Fatalf("group = %+v", g)
	}
	if s.last().Variables["name"] != "family" {
		t.Fatalf("variables = %v", s.last().Variables)
	}
}
