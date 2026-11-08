//go:build integration

package lldap_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jigfox/lldap-operator/internal/invite"
	"github.com/jigfox/lldap-operator/internal/lldap"
	"github.com/jigfox/lldap-operator/internal/lldap/lldaptest"
)

func TestAgainstRealLLDAP(t *testing.T) {
	ctx := context.Background()
	srv := lldaptest.Start(ctx, t)
	c := lldap.NewHTTPClient(srv.URL, srv.Username, srv.Password, nil)

	groups, err := c.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, g := range groups {
		names[g.DisplayName] = g.ID
	}
	for _, builtin := range []string{"lldap_admin", "lldap_password_manager", "lldap_strict_readonly"} {
		if _, ok := names[builtin]; !ok {
			t.Fatalf("built-in group %s missing in %v", builtin, names)
		}
	}

	if _, err := c.GetUser(ctx, "nobody"); !errors.Is(err, lldap.ErrNotFound) {
		t.Fatalf("GetUser(nobody) err = %v, want ErrNotFound", err)
	}

	g, err := c.CreateGroup(ctx, "Family")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateUser(ctx, lldap.CreateUserInput{
		ID: "alice", Email: "alice@example.com", DisplayName: "Alice", FirstName: "Al", LastName: "Ice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddUserToGroup(ctx, "alice", g.ID); err != nil {
		t.Fatal(err)
	}
	u, err := c.GetUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "alice@example.com" || u.DisplayName != "Alice" || u.FirstName != "Al" || u.LastName != "Ice" {
		t.Fatalf("user = %+v", u)
	}
	if len(u.Groups) != 1 || u.Groups[0].ID != g.ID {
		t.Fatalf("groups = %+v", u.Groups)
	}

	email, last := "alice@example.org", "Wonder"
	if err := c.UpdateUser(ctx, lldap.UpdateUserInput{ID: "alice", Email: &email, LastName: &last}); err != nil {
		t.Fatal(err)
	}
	if u, err = c.GetUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if u.Email != email || u.LastName != last || u.FirstName != "Al" {
		t.Fatalf("after update: %+v", u)
	}

	if err := c.UpdateGroup(ctx, g.ID, "Relatives"); err != nil {
		t.Fatal(err)
	}
	if u, err = c.GetUser(ctx, "alice"); err != nil || u.Groups[0].DisplayName != "Relatives" {
		t.Fatalf("after rename: %+v, %v", u, err)
	}

	if err := c.RemoveUserFromGroup(ctx, "alice", g.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ = c.GetUser(ctx, "alice"); len(u.Groups) != 0 {
		t.Fatalf("still member: %+v", u.Groups)
	}
	if err := c.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetUser(ctx, "alice"); !errors.Is(err, lldap.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// A wrong password never yields a token.
	bad := lldap.NewHTTPClient(srv.URL, srv.Username, "wrong", nil)
	if _, err := bad.ListGroups(ctx); err == nil {
		t.Fatal("login with a wrong password succeeded")
	}
}

// TestInviteAgainstRealLLDAP lets lldap send its password-reset mail to a
// throwaway SMTP server.
func TestInviteAgainstRealLLDAP(t *testing.T) {
	ctx := context.Background()
	srv, mail := lldaptest.StartWithMail(ctx, t)
	c := lldap.NewHTTPClient(srv.URL, srv.Username, srv.Password, nil)
	if err := c.CreateUser(ctx, lldap.CreateUserInput{ID: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}

	inv := invite.NewLLDAP(srv.URL, nil)
	if err := inv.Invite(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := mail.Messages(t); !slices.Equal(got, []string{"alice@example.com"}) {
		t.Fatalf("mails sent to %q", got)
	}
	// lldap answers 200 for unknown users and sends nothing.
	if err := inv.Invite(ctx, "nobody"); err != nil {
		t.Fatal(err)
	}
	if got := mail.Messages(t); len(got) != 1 {
		t.Fatalf("mails sent to %q", got)
	}

	// Without SMTP configured the endpoint does not exist.
	plain := lldaptest.Start(ctx, t)
	if err := invite.NewLLDAP(plain.URL, nil).Invite(ctx, "alice"); err == nil {
		t.Fatal("invite succeeded against an lldap without password reset")
	}
}
