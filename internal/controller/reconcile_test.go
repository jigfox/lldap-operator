package controller

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	"github.com/jigfox/lldap-operator/internal/lldap"
	fakelldap "github.com/jigfox/lldap-operator/internal/lldap/fake"
)

const ns = "lldap"

type env struct {
	t    *testing.T
	c    client.Client
	l    *fakelldap.Client
	rec  *events.FakeRecorder
	grp  *LLDAPGroupReconciler
	usr  *LLDAPUserReconciler
	last ctrl.Result
}

func newEnv(t *testing.T, dryRun bool, objs ...client.Object) *env {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := lldapv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&lldapv1alpha1.LLDAPGroup{}, &lldapv1alpha1.LLDAPUser{}).
		WithObjects(objs...).Build()
	l := fakelldap.New()
	rec := events.NewFakeRecorder(100)
	return &env{
		t: t, c: c, l: l, rec: rec,
		grp: &LLDAPGroupReconciler{Client: c, Scheme: scheme, LLDAP: l, Recorder: rec, DryRun: dryRun},
		usr: &LLDAPUserReconciler{
			Client: c, Scheme: scheme, LLDAP: l, Recorder: rec, DryRun: dryRun, ServiceUser: "operator",
		},
	}
}

func (e *env) reconcileGroup(name string) {
	e.t.Helper()
	res, err := e.grp.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	if err != nil {
		e.t.Fatalf("reconcile group %s: %v", name, err)
	}
	e.last = res
}

func (e *env) reconcileUser(name string) {
	e.t.Helper()
	res, err := e.usr.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	if err != nil {
		e.t.Fatalf("reconcile user %s: %v", name, err)
	}
	e.last = res
}

func (e *env) group(name string) *lldapv1alpha1.LLDAPGroup {
	e.t.Helper()
	var g lldapv1alpha1.LLDAPGroup
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &g); err != nil {
		e.t.Fatal(err)
	}
	return &g
}

func (e *env) user(name string) *lldapv1alpha1.LLDAPUser {
	e.t.Helper()
	var u lldapv1alpha1.LLDAPUser
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &u); err != nil {
		e.t.Fatal(err)
	}
	return &u
}

func (e *env) events() []string {
	var out []string
	for {
		select {
		case ev := <-e.rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func grp(name string, mod ...func(*lldapv1alpha1.LLDAPGroup)) *lldapv1alpha1.LLDAPGroup {
	g := &lldapv1alpha1.LLDAPGroup{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1}}
	for _, m := range mod {
		m(g)
	}
	return g
}

func usr(name string, mod ...func(*lldapv1alpha1.LLDAPUser)) *lldapv1alpha1.LLDAPUser {
	u := &lldapv1alpha1.LLDAPUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1},
		Spec:       lldapv1alpha1.LLDAPUserSpec{Email: name + "@example.com"},
	}
	for _, m := range mod {
		m(u)
	}
	return u
}

func ready(t *testing.T, conds []metav1.Condition) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(conds, lldapv1alpha1.ConditionReady)
	if c == nil {
		t.Fatal("no Ready condition")
	}
	return c
}

func expectCalls(t *testing.T, l *fakelldap.Client, want ...string) {
	t.Helper()
	got := l.Calls()
	if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Fatalf("lldap calls = %q, want %q", got, want)
	}
}

func hasEvent(evs []string, sub string) bool {
	return slices.ContainsFunc(evs, func(e string) bool { return strings.Contains(e, sub) })
}

// ---- groups ----

func TestGroupCreate(t *testing.T) {
	e := newEnv(t, false, grp("family"))
	e.reconcileGroup("family")
	expectCalls(t, e.l, "CreateGroup family")
	g := e.group("family")
	if g.Status.GroupID == 0 || g.Status.Adopted || g.Status.DisplayName != "family" {
		t.Fatalf("status = %+v", g.Status)
	}
	if c := ready(t, g.Status.Conditions); c.Status != metav1.ConditionTrue || c.ObservedGeneration != 1 {
		t.Fatalf("ready = %+v", c)
	}
	if !slices.Contains(g.Finalizers, Finalizer) {
		t.Fatal("finalizer missing")
	}
	if e.last.RequeueAfter != DefaultInterval {
		t.Fatalf("requeue = %v", e.last.RequeueAfter)
	}
	// Second reconcile is a no-op.
	e.l.ResetCalls()
	e.reconcileGroup("family")
	expectCalls(t, e.l)
}

func TestGroupAdoptExisting(t *testing.T) {
	e := newEnv(t, false, grp("family"))
	id := e.l.SeedGroup("family")
	e.reconcileGroup("family")
	expectCalls(t, e.l)
	g := e.group("family")
	if !g.Status.Adopted || g.Status.GroupID != id {
		t.Fatalf("status = %+v", g.Status)
	}
}

func TestGroupCustomDisplayNameAndRename(t *testing.T) {
	e := newEnv(t, false, grp("g", func(g *lldapv1alpha1.LLDAPGroup) { g.Spec.DisplayName = "old" }))
	e.reconcileGroup("g")
	id := e.group("g").Status.GroupID

	g := e.group("g")
	g.Spec.DisplayName = "new"
	g.Generation = 2
	if err := e.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	e.l.ResetCalls()
	e.reconcileGroup("g")
	expectCalls(t, e.l, "UpdateGroup "+itoa(id)+" new")
	if got := e.group("g"); got.Status.GroupID != id || got.Status.DisplayName != "new" {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestGroupRenameConflict(t *testing.T) {
	e := newEnv(t, false, grp("g", func(g *lldapv1alpha1.LLDAPGroup) { g.Spec.DisplayName = "a" }))
	e.reconcileGroup("g")
	e.l.SeedGroup("b")
	g := e.group("g")
	g.Spec.DisplayName = "b"
	if err := e.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	e.l.ResetCalls()
	e.reconcileGroup("g")
	expectCalls(t, e.l)
	if c := ready(t, e.group("g").Status.Conditions); c.Status != metav1.ConditionFalse || c.Reason != lldapv1alpha1.ReasonInvalid {
		t.Fatalf("ready = %+v", c)
	}
}

func TestGroupReserved(t *testing.T) {
	e := newEnv(t, false, grp("lldap_admin"))
	e.reconcileGroup("lldap_admin")
	expectCalls(t, e.l)
	if c := ready(t, e.group("lldap_admin").Status.Conditions); c.Reason != lldapv1alpha1.ReasonInvalid {
		t.Fatalf("ready = %+v", c)
	}
}

func TestGroupDeletion(t *testing.T) {
	for _, tt := range []struct {
		policy lldapv1alpha1.DeletionPolicy
		want   []string
	}{
		{"", []string{"DeleteGroup"}},
		{lldapv1alpha1.DeletionPolicyDelete, []string{"DeleteGroup"}},
		{lldapv1alpha1.DeletionPolicyOrphan, nil},
	} {
		t.Run(string(tt.policy), func(t *testing.T) {
			e := newEnv(t, false, grp("family", func(g *lldapv1alpha1.LLDAPGroup) { g.Spec.DeletionPolicy = tt.policy }))
			e.reconcileGroup("family")
			e.l.ResetCalls()
			if err := e.c.Delete(context.Background(), e.group("family")); err != nil {
				t.Fatal(err)
			}
			e.reconcileGroup("family")
			got := e.l.Calls()
			if len(got) != len(tt.want) || (len(got) == 1 && !strings.HasPrefix(got[0], tt.want[0])) {
				t.Fatalf("calls = %q, want %q", got, tt.want)
			}
			var g lldapv1alpha1.LLDAPGroup
			err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "family"}, &g)
			if err == nil {
				t.Fatal("resource still exists, finalizer not removed")
			}
		})
	}
}

func TestGroupDeletionNeverRemovesBuiltIn(t *testing.T) {
	g := grp("x", func(g *lldapv1alpha1.LLDAPGroup) { g.Finalizers = []string{Finalizer} })
	g.Status.GroupID = 1 // lldap_admin in the fake
	g.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	e := newEnv(t, false, g)
	e.reconcileGroup("x")
	expectCalls(t, e.l)
	if !slices.Contains(e.l.GroupNames(), "lldap_admin") {
		t.Fatal("built-in group removed")
	}
}

func TestGroupDryRun(t *testing.T) {
	e := newEnv(t, true, grp("new"), grp("existing"))
	e.l.SeedGroup("existing")
	e.reconcileGroup("new")
	e.reconcileGroup("existing")
	expectCalls(t, e.l)
	if slices.Contains(e.l.GroupNames(), "new") {
		t.Fatal("group created in dry-run")
	}
	n := e.group("new")
	if c := ready(t, n.Status.Conditions); c.Status != metav1.ConditionFalse || c.Reason != lldapv1alpha1.ReasonDryRunPending {
		t.Fatalf("ready = %+v", c)
	}
	if !meta.IsStatusConditionTrue(n.Status.Conditions, lldapv1alpha1.ConditionDryRun) {
		t.Fatal("DryRun condition missing")
	}
	// Adoption of an unchanged group: nothing pending, so Ready.
	x := e.group("existing")
	if c := ready(t, x.Status.Conditions); c.Status != metav1.ConditionTrue || !x.Status.Adopted {
		t.Fatalf("existing: ready = %+v adopted=%v", c, x.Status.Adopted)
	}
	if !hasEvent(e.events(), "DryRun: would create group \"new\"") {
		t.Fatal("dry-run event missing")
	}
}

func TestGroupDryRunDeleteSendsNothing(t *testing.T) {
	e := newEnv(t, true, grp("existing"))
	e.l.SeedGroup("existing")
	e.reconcileGroup("existing")
	if err := e.c.Delete(context.Background(), e.group("existing")); err != nil {
		t.Fatal(err)
	}
	e.reconcileGroup("existing")
	expectCalls(t, e.l)
	if !hasEvent(e.events(), "DryRun: would delete group") {
		t.Fatal("dry-run event missing")
	}
}

// ---- users ----

func withGroups(g ...string) func(*lldapv1alpha1.LLDAPUser) {
	return func(u *lldapv1alpha1.LLDAPUser) { u.Spec.Groups = g }
}

func TestUserCreateWithMemberships(t *testing.T) {
	e := newEnv(t, false, grp("family"), usr("alice", withGroups("family"), func(u *lldapv1alpha1.LLDAPUser) {
		u.Spec.DisplayName = "Alice"
	}))
	e.reconcileGroup("family")
	e.l.ResetCalls()
	e.reconcileUser("alice")
	expectCalls(t, e.l, "CreateUser alice", "AddUserToGroup alice family")
	u := e.user("alice")
	if u.Status.UserID != "alice" || u.Status.Adopted || !slices.Equal(u.Status.Groups, []string{"family"}) {
		t.Fatalf("status = %+v", u.Status)
	}
	if c := ready(t, u.Status.Conditions); c.Status != metav1.ConditionTrue {
		t.Fatalf("ready = %+v", c)
	}
	if got := e.l.User("alice"); got.DisplayName != "Alice" || got.Email != "alice@example.com" {
		t.Fatalf("lldap user = %+v", got)
	}
	e.l.ResetCalls()
	e.reconcileUser("alice")
	expectCalls(t, e.l)
}

func TestUserAdoptKeepsUnmanagedMemberships(t *testing.T) {
	e := newEnv(t, false, grp("family"), usr("alice", withGroups("family")))
	e.reconcileGroup("family")
	// Existing user with admin rights granted outside the operator and a
	// display name the CR does not set.
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com", DisplayName: "Alice A", FirstName: "Al"},
		"lldap_admin", "other")
	e.l.ResetCalls()
	e.reconcileUser("alice")
	expectCalls(t, e.l, "AddUserToGroup alice family")
	u := e.user("alice")
	if !u.Status.Adopted {
		t.Fatal("not adopted")
	}
	got := e.l.User("alice")
	if got.DisplayName != "Alice A" || got.FirstName != "Al" {
		t.Fatalf("empty spec fields must not blank lldap values: %+v", got)
	}
	names := make([]string, 0, len(got.Groups))
	for _, g := range got.Groups {
		names = append(names, g.DisplayName)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"family", "lldap_admin", "other"}) {
		t.Fatalf("groups = %v", names)
	}
}

func TestUserUpdateFields(t *testing.T) {
	e := newEnv(t, false, usr("alice", func(u *lldapv1alpha1.LLDAPUser) {
		u.Spec.Email = "new@example.com"
		u.Spec.FirstName = "Alice"
		u.Spec.LastName = "Smith"
	}))
	e.l.Seed(lldap.User{ID: "alice", Email: "old@example.com", FirstName: "Alice", LastName: "Jones"})
	e.reconcileUser("alice")
	expectCalls(t, e.l, "UpdateUser alice")
	if got := e.l.User("alice"); got.Email != "new@example.com" || got.LastName != "Smith" {
		t.Fatalf("lldap user = %+v", got)
	}
}

func TestUserMembershipRemoveOnlyManaged(t *testing.T) {
	e := newEnv(t, false, grp("a"), grp("b"), usr("alice", withGroups("a")))
	e.reconcileGroup("a")
	e.reconcileGroup("b")
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com"}, "a", "b", "lldap_admin", "unmanaged")
	e.l.ResetCalls()
	e.reconcileUser("alice")
	// b is managed but no longer listed -> removed. lldap_admin and unmanaged stay.
	expectCalls(t, e.l, "RemoveUserFromGroup alice b")
	if got := e.user("alice").Status.Groups; !slices.Equal(got, []string{"a"}) {
		t.Fatalf("status.groups = %v", got)
	}
}

func TestUserUnknownGroup(t *testing.T) {
	e := newEnv(t, false, usr("alice", withGroups("nope", "lldap_admin")))
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com"})
	e.reconcileUser("alice")
	expectCalls(t, e.l) // nothing added, not even to the built-in group
	c := ready(t, e.user("alice").Status.Conditions)
	if c.Status != metav1.ConditionFalse || c.Reason != lldapv1alpha1.ReasonUnknownGroup ||
		!strings.Contains(c.Message, "nope") || !strings.Contains(c.Message, "lldap_admin") {
		t.Fatalf("ready = %+v", c)
	}
}

func TestUserGroupPendingUntilGroupExists(t *testing.T) {
	e := newEnv(t, false, grp("family"), usr("alice", withGroups("family")))
	e.reconcileUser("alice") // group CR exists but was not reconciled yet
	expectCalls(t, e.l, "CreateUser alice")
	c := ready(t, e.user("alice").Status.Conditions)
	if c.Reason != ReasonGroupPending || e.last.RequeueAfter != pendingRetry {
		t.Fatalf("ready = %+v requeue = %v", c, e.last.RequeueAfter)
	}
	e.reconcileGroup("family")
	e.reconcileUser("alice")
	if c := ready(t, e.user("alice").Status.Conditions); c.Status != metav1.ConditionTrue {
		t.Fatalf("ready = %+v", c)
	}
}

func TestUserRefusesServiceUserAndUsernameChange(t *testing.T) {
	e := newEnv(t, false, usr("svc", func(u *lldapv1alpha1.LLDAPUser) { u.Spec.Username = "operator" }))
	e.l.Seed(lldap.User{ID: "operator", Email: "o@example.com"})
	e.reconcileUser("svc")
	expectCalls(t, e.l)
	if c := ready(t, e.user("svc").Status.Conditions); c.Reason != lldapv1alpha1.ReasonInvalid {
		t.Fatalf("ready = %+v", c)
	}

	e = newEnv(t, false, usr("bob"))
	e.reconcileUser("bob")
	u := e.user("bob")
	u.Spec.Username = "robert"
	if err := e.c.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.l.ResetCalls()
	e.reconcileUser("bob")
	expectCalls(t, e.l)
	if c := ready(t, e.user("bob").Status.Conditions); c.Reason != lldapv1alpha1.ReasonInvalid {
		t.Fatalf("ready = %+v", c)
	}
}

func TestUserDeletion(t *testing.T) {
	for _, tt := range []struct {
		policy lldapv1alpha1.DeletionPolicy
		gone   bool
	}{
		{"", true},
		{lldapv1alpha1.DeletionPolicyDelete, true},
		{lldapv1alpha1.DeletionPolicyOrphan, false},
	} {
		t.Run(string(tt.policy), func(t *testing.T) {
			e := newEnv(t, false, usr("alice", func(u *lldapv1alpha1.LLDAPUser) { u.Spec.DeletionPolicy = tt.policy }))
			e.reconcileUser("alice")
			if err := e.c.Delete(context.Background(), e.user("alice")); err != nil {
				t.Fatal(err)
			}
			e.reconcileUser("alice")
			if (e.l.User("alice") == nil) != tt.gone {
				t.Fatalf("user gone = %v, want %v", e.l.User("alice") == nil, tt.gone)
			}
			var u lldapv1alpha1.LLDAPUser
			if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "alice"}, &u); err == nil {
				t.Fatal("finalizer not removed")
			}
		})
	}
}

func TestUserDeletionNeverTouchesUnadoptedLeftover(t *testing.T) {
	// A CR that never reached lldap (no status.userID) must not delete a
	// user that happens to share its name.
	u := usr("alice", func(u *lldapv1alpha1.LLDAPUser) { u.Finalizers = []string{Finalizer} })
	u.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	e := newEnv(t, false, u)
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com"})
	e.reconcileUser("alice")
	expectCalls(t, e.l)
}

func TestUserDryRunSendsNothing(t *testing.T) {
	e := newEnv(t, true, grp("family"), usr("alice", withGroups("family")), usr("bob", withGroups("family")))
	e.l.SeedGroup("family")
	e.l.Seed(lldap.User{ID: "bob", Email: "bob@example.com"}) // adopted, but needs a membership
	e.reconcileGroup("family")
	e.reconcileUser("alice")
	e.reconcileUser("bob")
	expectCalls(t, e.l)
	if e.l.User("alice") != nil {
		t.Fatal("user created in dry-run")
	}
	for _, n := range []string{"alice", "bob"} {
		u := e.user(n)
		if c := ready(t, u.Status.Conditions); c.Reason != lldapv1alpha1.ReasonDryRunPending {
			t.Fatalf("%s ready = %+v", n, c)
		}
		if !meta.IsStatusConditionTrue(u.Status.Conditions, lldapv1alpha1.ConditionDryRun) {
			t.Fatalf("%s: DryRun condition missing", n)
		}
	}
	evs := e.events()
	for _, want := range []string{
		`DryRun: would create user "alice"`,
		`DryRun: would add member "alice" to group "family"`,
		`DryRun: would add member "bob" to group "family"`,
	} {
		if !hasEvent(evs, want) {
			t.Errorf("event %q missing in %q", want, evs)
		}
	}
}

func TestUserDryRunAdoptionIsNoOp(t *testing.T) {
	e := newEnv(t, true, grp("family"), usr("alice", withGroups("family")))
	gid := e.l.SeedGroup("family")
	e.l.Seed(lldap.User{ID: "alice", Email: "alice@example.com"}, "family")
	_ = gid
	e.reconcileGroup("family")
	e.reconcileUser("alice")
	expectCalls(t, e.l)
	u := e.user("alice")
	if c := ready(t, u.Status.Conditions); c.Status != metav1.ConditionTrue || !u.Status.Adopted {
		t.Fatalf("ready = %+v adopted = %v", c, u.Status.Adopted)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
