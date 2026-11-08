package controller

import (
	"context"
	"errors"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	fakeinvite "github.com/jigfox/lldap-operator/internal/invite/fake"
	"github.com/jigfox/lldap-operator/internal/lldap"
)

func newInviteEnv(t *testing.T, dryRun bool, objs ...client.Object) (*env, *fakeinvite.Inviter) {
	t.Helper()
	e := newEnv(t, dryRun, objs...)
	inv := &fakeinvite.Inviter{}
	e.usr.Inviter = inv
	return e, inv
}

func noInvitation(v bool) func(*lldapv1alpha1.LLDAPUser) {
	return func(u *lldapv1alpha1.LLDAPUser) { u.Spec.SendInvitation = &v }
}

func invitation(t *testing.T, u *lldapv1alpha1.LLDAPUser) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(u.Status.Conditions, lldapv1alpha1.ConditionInvitationSent)
}

func TestInvitationOnCreateOnce(t *testing.T) {
	e, inv := newInviteEnv(t, false, usr("alice"))
	e.reconcileUser("alice")
	if got := inv.Calls(); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("invitations = %q", got)
	}
	u := e.user("alice")
	if c := invitation(t, u); c == nil || c.Status != metav1.ConditionTrue || c.Reason != lldapv1alpha1.ReasonInvitationSent {
		t.Fatalf("condition = %+v", c)
	}
	if u.Status.InvitationSentAt == nil {
		t.Fatal("invitationSentAt not recorded")
	}
	if !hasEvent(e.events(), "Sent invitation to user \"alice\"") {
		t.Fatal("event missing")
	}
	// Re-reconciles, also after a spec change, never send again.
	e.reconcileUser("alice")
	u = e.user("alice")
	u.Generation = 2
	u.Spec.DisplayName = "Alice"
	if err := e.c.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 1 {
		t.Fatalf("invitations = %q", got)
	}
}

func TestInvitationNotForAdoptedUser(t *testing.T) {
	e, inv := newInviteEnv(t, false, usr("alice"))
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com"})
	e.reconcileUser("alice")
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 0 {
		t.Fatalf("invitations = %q", got)
	}
	if u := e.user("alice"); invitation(t, u) != nil || u.Status.InvitationSentAt != nil {
		t.Fatalf("status = %+v", u.Status)
	}
}

func TestInvitationSuppressedPerUser(t *testing.T) {
	e, inv := newInviteEnv(t, false, usr("alice", noInvitation(false)))
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 0 {
		t.Fatalf("invitations = %q", got)
	}
	if u := e.user("alice"); invitation(t, u) != nil || e.l.User("alice") == nil {
		t.Fatalf("status = %+v", u.Status)
	}
}

func TestInvitationDisabledByOperator(t *testing.T) {
	e := newEnv(t, false, usr("alice")) // no Inviter: --invite=none
	e.reconcileUser("alice")
	if u := e.user("alice"); invitation(t, u) != nil {
		t.Fatalf("status = %+v", u.Status)
	}
}

func TestInvitationFailureIsRetried(t *testing.T) {
	e, inv := newInviteEnv(t, false, grp("family"), usr("alice", withGroups("family")))
	e.reconcileGroup("family")
	inv.SetError(errors.New("smtp down"))
	_, err := e.usr.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "alice"}})
	if err == nil {
		t.Fatal("failed invitation not reported for a retry with backoff")
	}
	// The user and its groups converged regardless.
	u := e.user("alice")
	if e.l.User("alice") == nil || !slices.Equal(u.Status.Groups, []string{"family"}) {
		t.Fatalf("status = %+v", u.Status)
	}
	if c := ready(t, u.Status.Conditions); c.Status != metav1.ConditionTrue {
		t.Fatalf("ready = %+v", c)
	}
	c := invitation(t, u)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != lldapv1alpha1.ReasonInvitationFailed || c.Message != "smtp down" {
		t.Fatalf("condition = %+v", c)
	}
	if u.Status.InvitationSentAt != nil {
		t.Fatal("failed invitation recorded as sent")
	}
	if !hasEvent(e.events(), "Warning") {
		t.Fatal("warning event missing")
	}

	inv.SetError(nil)
	e.reconcileUser("alice")
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 2 {
		t.Fatalf("invitations = %q, want the failed one and one retry", got)
	}
	u = e.user("alice")
	if c := invitation(t, u); c.Status != metav1.ConditionTrue || u.Status.InvitationSentAt == nil {
		t.Fatalf("status = %+v", u.Status)
	}
}

func TestInvitationSwitchedOffWhilePending(t *testing.T) {
	e, inv := newInviteEnv(t, false, usr("alice"))
	inv.SetError(errors.New("smtp down"))
	_, _ = e.usr.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "alice"}})
	u := e.user("alice")
	u.Spec.SendInvitation = new(bool)
	if err := e.c.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 1 {
		t.Fatalf("invitations = %q", got)
	}
	if invitation(t, e.user("alice")) != nil {
		t.Fatal("pending marker left behind")
	}
}

func TestInvitationDryRun(t *testing.T) {
	e, inv := newInviteEnv(t, true, usr("alice"))
	e.reconcileUser("alice")
	e.reconcileUser("alice")
	if got := inv.Calls(); len(got) != 0 {
		t.Fatalf("invitations = %q", got)
	}
	if !hasEvent(e.events(), `DryRun: would send invitation to user "alice"`) {
		t.Fatal("dry-run event missing")
	}
	if u := e.user("alice"); invitation(t, u) != nil || u.Status.InvitationSentAt != nil {
		t.Fatalf("status = %+v", u.Status)
	}
}
