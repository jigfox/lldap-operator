//go:build integration

package controller

import (
	"context"
	"fmt"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	"github.com/jigfox/lldap-operator/internal/lldap"
	"github.com/jigfox/lldap-operator/internal/lldap/lldaptest"
)

// TestReconcileAgainstRealLLDAP runs the reconcilers (with a fake Kubernetes
// client) against a real lldap: adoption, convergence, idempotence, deletion.
func TestReconcileAgainstRealLLDAP(t *testing.T) {
	ctx := context.Background()
	srv := lldaptest.Start(ctx, t)
	real := lldap.NewHTTPClient(srv.URL, srv.Username, srv.Password, nil)

	// Pre-existing state, as when adopting an existing directory.
	g, err := real.CreateGroup(ctx, "Family")
	if err != nil {
		t.Fatal(err)
	}
	if err := real.CreateUser(ctx, lldap.CreateUserInput{ID: "alice", Email: "Alice@Example.com", DisplayName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := real.AddUserToGroup(ctx, "alice", g.ID); err != nil {
		t.Fatal(err)
	}

	e := newEnv(t, false,
		grp("family"), // different case than the lldap group: still the same group
		grp("friends"),
		usr("alice", withGroups("family", "friends")),
		usr("bob", withGroups("friends"), func(u *lldapv1alpha1.LLDAPUser) { u.Spec.FirstName = "Bob" }),
	)
	e.grp.LLDAP, e.usr.LLDAP = real, real

	e.reconcileGroup("family")
	e.reconcileGroup("friends")
	e.reconcileUser("alice")
	e.reconcileUser("bob")

	if gr := e.group("family"); !gr.Status.Adopted || gr.Status.GroupID != g.ID {
		t.Fatalf("family: %+v", gr.Status)
	}
	if gr := e.group("friends"); gr.Status.Adopted || gr.Status.GroupID == 0 {
		t.Fatalf("friends: %+v", gr.Status)
	}
	if u := e.user("alice"); !u.Status.Adopted || !slices.Equal(u.Status.Groups, []string{"Family", "friends"}) {
		t.Fatalf("alice: %+v", u.Status)
	}
	if u := e.user("bob"); u.Status.Adopted || u.Status.UserID != "bob" {
		t.Fatalf("bob: %+v", u.Status)
	}
	for _, n := range []string{"alice", "bob"} {
		if c := ready(t, e.user(n).Status.Conditions); c.Status != metav1.ConditionTrue {
			t.Fatalf("%s: %+v", n, c)
		}
	}
	// Adoption must not have rewritten what only differs in case.
	if u, _ := real.GetUser(ctx, "alice"); u.DisplayName != "Alice" {
		t.Fatalf("alice: %+v", u)
	}

	// A second pass changes nothing (no flapping on case differences).
	before := snapshot(t, real)
	e.reconcileGroup("family")
	e.reconcileUser("alice")
	if after := snapshot(t, real); after != before {
		t.Fatalf("second reconcile changed lldap:\n%s\n%s", before, after)
	}

	// Remove alice's membership of friends, rename family, delete bob.
	alice := e.user("alice")
	alice.Spec.Groups = []string{"family"}
	if err := e.c.Update(ctx, alice); err != nil {
		t.Fatal(err)
	}
	fam := e.group("family")
	fam.Spec.DisplayName = "Relatives"
	if err := e.c.Update(ctx, fam); err != nil {
		t.Fatal(err)
	}
	e.reconcileGroup("family")
	alice = e.user("alice")
	alice.Spec.Groups = []string{"Relatives"}
	if err := e.c.Update(ctx, alice); err != nil {
		t.Fatal(err)
	}
	e.reconcileUser("alice")
	if u, _ := real.GetUser(ctx, "alice"); len(u.Groups) != 1 || u.Groups[0].DisplayName != "Relatives" || u.Groups[0].ID != g.ID {
		t.Fatalf("alice groups: %+v", u.Groups)
	}
	if err := e.c.Delete(ctx, e.user("bob")); err != nil {
		t.Fatal(err)
	}
	e.reconcileUser("bob")
	if _, err := real.GetUser(ctx, "bob"); err == nil {
		t.Fatal("bob still exists")
	}
}

func snapshot(t *testing.T, c lldap.Client) string {
	t.Helper()
	u, err := c.GetUser(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := c.ListGroups(context.Background())
	return formatSnapshot(u, groups)
}

func formatSnapshot(u *lldap.User, groups []lldap.Group) string {
	return fmt.Sprintf("%+v %+v", *u, groups)
}
