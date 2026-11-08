package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	"github.com/jigfox/lldap-operator/internal/invite"
	"github.com/jigfox/lldap-operator/internal/lldap"
)

// ReasonGroupPending is used while a listed group has an LLDAPGroup that does
// not exist in lldap yet.
const ReasonGroupPending = "GroupPending"

// LLDAPUserReconciler reconciles a LLDAPUser object
type LLDAPUserReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	LLDAP    lldap.Client
	Recorder events.EventRecorder
	DryRun   bool
	Interval time.Duration
	// ServiceUser is the lldap user the operator logs in as; it is never managed.
	ServiceUser string
	// Inviter triggers the password-reset mail for users this operator creates;
	// nil disables invitations (--invite=none).
	Inviter invite.Inviter
}

// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapusers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups,verbs=get;list;watch

func (r *LLDAPUserReconciler) shared() *shared {
	return &shared{LLDAP: r.LLDAP, Recorder: r.Recorder, DryRun: r.DryRun, Interval: r.Interval}
}

// Reconcile makes the lldap user and its memberships match the LLDAPUser.
func (r *LLDAPUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var user lldapv1alpha1.LLDAPUser
	if err := r.Get(ctx, req.NamespacedName, &user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	s := r.shared()

	if !user.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, s, &user)
	}
	if controllerutil.AddFinalizer(&user, Finalizer) {
		if err := r.Update(ctx, &user); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := user.DeepCopy()
	pending := 0
	res, err := r.sync(ctx, s, &user, &pending)
	user.Status.ObservedGeneration = user.Generation
	if err != nil {
		res = syncResult{reason: lldapv1alpha1.ReasonLLDAPError, msg: err.Error()}
	}
	s.setConditions(&user.Status.Conditions, user.Generation, res.ready, res.reason, res.msg, pending)
	if perr := r.Status().Patch(ctx, &user, client.MergeFrom(orig)); perr != nil {
		if apierrors.IsNotFound(perr) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, perr
	}
	// The mail never blocks the sync above: user and groups have converged and
	// their status is stored before it is attempted. A failure is retried with
	// the controller's backoff.
	if ierr := r.invite(ctx, s, &user); ierr != nil && err == nil {
		return ctrl.Result{}, ierr
	}
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case res.reason == lldapv1alpha1.ReasonInvalid:
		return ctrl.Result{}, nil
	case res.reason == ReasonGroupPending:
		return ctrl.Result{RequeueAfter: min(pendingRetry, s.interval())}, nil
	}
	return ctrl.Result{RequeueAfter: s.interval()}, nil
}

type syncResult struct {
	ready  bool
	reason string
	msg    string
}

func invalid(format string, args ...any) syncResult {
	return syncResult{reason: lldapv1alpha1.ReasonInvalid, msg: fmt.Sprintf(format, args...)}
}

// sync creates/adopts the user, converges its fields and its memberships in
// managed groups. It fills user.Status.
func (r *LLDAPUserReconciler) sync(ctx context.Context, s *shared, user *lldapv1alpha1.LLDAPUser,
	pending *int) (syncResult, error) {
	name := user.EffectiveUsername()
	if r.ServiceUser != "" && strings.EqualFold(name, r.ServiceUser) {
		return invalid("user %q is the operator's own lldap account and cannot be managed", name), nil
	}
	if user.Status.UserID != "" && !strings.EqualFold(user.Status.UserID, name) {
		return invalid("username is immutable: this resource manages %q, spec asks for %q",
			user.Status.UserID, name), nil
	}

	lu, err := r.LLDAP.GetUser(ctx, name)
	switch {
	case errors.Is(err, lldap.ErrNotFound):
		lu = nil
	case err != nil:
		return syncResult{}, err
	}

	if lu == nil {
		did, err := s.run(ctx, user, pending, "Create", fmt.Sprintf("create user %q", name), func() error {
			return r.LLDAP.CreateUser(ctx, lldap.CreateUserInput{
				ID: name, Email: user.Spec.Email, DisplayName: user.Spec.DisplayName,
				FirstName: user.Spec.FirstName, LastName: user.Spec.LastName,
			})
		})
		if err != nil {
			return syncResult{}, err
		}
		switch {
		case did:
			// Recorded before anything else can fail, so the next reconcile sees a
			// user this operator created instead of one to adopt.
			user.Status.UserID = name
			r.requestInvitation(user)
			if lu, err = r.LLDAP.GetUser(ctx, name); err != nil {
				return syncResult{}, err
			}
		case s.DryRun && r.invitationWanted(user):
			logf.FromContext(ctx).Info("dry-run: would send invitation", "user", name)
			s.Recorder.Eventf(user, nil, corev1.EventTypeNormal, reasonDryRun, "Invite",
				"DryRun: would send invitation to user %q", name)
		}
	} else {
		if user.Status.UserID == "" {
			user.Status.Adopted = true
			s.Recorder.Eventf(user, nil, "Normal", "Adopted", "Adopt", "Adopted existing lldap user %q", lu.ID)
		}
		if err := r.convergeFields(ctx, s, user, lu, pending); err != nil {
			return syncResult{}, err
		}
	}
	if lu != nil {
		user.Status.UserID = lu.ID
	}

	return r.convergeMemberships(ctx, s, user, name, lu, pending)
}

// invitationWanted is true if the operator should invite this user once it
// has created it.
func (r *LLDAPUserReconciler) invitationWanted(user *lldapv1alpha1.LLDAPUser) bool {
	return r.Inviter != nil && (user.Spec.SendInvitation == nil || *user.Spec.SendInvitation)
}

// requestInvitation marks a user that was just created as due for an
// invitation. Only creation does this, never adoption, and the marker is a
// condition so that a retry after a failed mail still knows the user is ours
// to invite.
func (r *LLDAPUserReconciler) requestInvitation(user *lldapv1alpha1.LLDAPUser) {
	if !r.invitationWanted(user) || user.Status.InvitationSentAt != nil {
		return
	}
	meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
		Type: lldapv1alpha1.ConditionInvitationSent, Status: metav1.ConditionFalse,
		Reason: lldapv1alpha1.ReasonInvitationPending, Message: "invitation not sent yet",
		ObservedGeneration: user.Generation,
	})
}

// invite sends the invitation of a user that requestInvitation marked.
//
// A mail cannot be taken back, a missing one can be re-sent by hand, so the
// order is chosen to never send twice: creation is stored first (by the
// caller), then invitationSentAt is claimed with an optimistic-lock patch (a
// conflict means another writer got in: nothing is sent, the retry decides
// again), and only then is the mail sent. If sending fails the claim is
// released and the user is retried; if the process dies between claim and
// send, or the claim cannot be released, the user is left uninvited rather
// than invited twice.
func (r *LLDAPUserReconciler) invite(ctx context.Context, s *shared, user *lldapv1alpha1.LLDAPUser) error {
	c := meta.FindStatusCondition(user.Status.Conditions, lldapv1alpha1.ConditionInvitationSent)
	if c == nil || c.Status == metav1.ConditionTrue || user.Status.InvitationSentAt != nil ||
		user.Status.Adopted || user.Status.UserID == "" {
		return nil
	}
	if s.DryRun {
		return nil
	}
	if !r.invitationWanted(user) {
		// Switched off after the user was created: drop the pending marker.
		orig := user.DeepCopy()
		meta.RemoveStatusCondition(&user.Status.Conditions, lldapv1alpha1.ConditionInvitationSent)
		return ignoreGone(r.Status().Patch(ctx, user, client.MergeFrom(orig)))
	}

	name := user.Status.UserID
	orig := user.DeepCopy()
	now := metav1.Now()
	user.Status.InvitationSentAt = &now
	if err := r.Status().Patch(ctx, user, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return ignoreGone(err)
	}

	orig = user.DeepCopy()
	if err := r.Inviter.Invite(ctx, name); err != nil {
		user.Status.InvitationSentAt = nil
		meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
			Type: lldapv1alpha1.ConditionInvitationSent, Status: metav1.ConditionFalse,
			Reason: lldapv1alpha1.ReasonInvitationFailed, Message: err.Error(),
			ObservedGeneration: user.Generation,
		})
		s.Recorder.Eventf(user, nil, corev1.EventTypeWarning, lldapv1alpha1.ReasonInvitationFailed, "Invite",
			"Sending the invitation to user %q failed: %v", name, err)
		if perr := r.Status().Patch(ctx, user, client.MergeFrom(orig)); perr != nil && !apierrors.IsNotFound(perr) {
			return errors.Join(err, perr)
		}
		return err
	}
	logf.FromContext(ctx).Info("sent invitation", "user", name)
	s.Recorder.Eventf(user, nil, corev1.EventTypeNormal, lldapv1alpha1.ReasonInvitationSent, "Invite",
		"Sent invitation to user %q", name)
	meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
		Type: lldapv1alpha1.ConditionInvitationSent, Status: metav1.ConditionTrue,
		Reason: lldapv1alpha1.ReasonInvitationSent, Message: "invitation sent",
		ObservedGeneration: user.Generation,
	})
	return ignoreGone(r.Status().Patch(ctx, user, client.MergeFrom(orig)))
}

func ignoreGone(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *LLDAPUserReconciler) convergeFields(ctx context.Context, s *shared, user *lldapv1alpha1.LLDAPUser,
	lu *lldap.User, pending *int) error {
	in := lldap.UpdateUserInput{ID: lu.ID}
	var changed []string
	if !strings.EqualFold(lu.Email, user.Spec.Email) {
		in.Email = &user.Spec.Email
		changed = append(changed, "email")
	}
	// Empty optional fields are left alone, so adopting a user never blanks them.
	if user.Spec.DisplayName != "" && lu.DisplayName != user.Spec.DisplayName {
		in.DisplayName = &user.Spec.DisplayName
		changed = append(changed, "displayName")
	}
	if user.Spec.FirstName != "" && lu.FirstName != user.Spec.FirstName {
		in.FirstName = &user.Spec.FirstName
		changed = append(changed, "firstName")
	}
	if user.Spec.LastName != "" && lu.LastName != user.Spec.LastName {
		in.LastName = &user.Spec.LastName
		changed = append(changed, "lastName")
	}
	if len(changed) == 0 {
		return nil
	}
	_, err := s.run(ctx, user, pending, "Update",
		fmt.Sprintf("update user %q (%s)", lu.ID, strings.Join(changed, ", ")),
		func() error { return r.LLDAP.UpdateUser(ctx, in) })
	return err
}

// convergeMemberships adds and removes memberships, but only in groups that
// an LLDAPGroup manages.
func (r *LLDAPUserReconciler) convergeMemberships(ctx context.Context, s *shared, user *lldapv1alpha1.LLDAPUser,
	userID string, lu *lldap.User, pending *int) (syncResult, error) {
	var crs lldapv1alpha1.LLDAPGroupList
	if err := r.List(ctx, &crs); err != nil {
		return syncResult{}, err
	}
	managed := map[string]bool{} // lower-case display name -> has an LLDAPGroup
	for i := range crs.Items {
		if crs.Items[i].DeletionTimestamp.IsZero() {
			managed[strings.ToLower(crs.Items[i].EffectiveDisplayName())] = true
		}
	}
	groups, err := r.LLDAP.ListGroups(ctx)
	if err != nil {
		return syncResult{}, err
	}

	// Desired: listed groups with an LLDAPGroup that exists in lldap.
	desired := map[int64]string{}
	var unknown, notYet []string
	for _, want := range user.Spec.Groups {
		g := findGroupByName(groups, want)
		switch {
		case !managed[strings.ToLower(want)] || (g != nil && hasReservedPrefix(g.DisplayName)):
			unknown = append(unknown, want)
		case g == nil:
			notYet = append(notYet, want)
		default:
			desired[g.ID] = g.DisplayName
		}
	}

	current := map[int64]string{}
	if lu != nil {
		for _, g := range lu.Groups {
			current[g.ID] = g.DisplayName
		}
	}
	actual := map[int64]string{}
	for id, n := range current {
		if managed[strings.ToLower(n)] && !hasReservedPrefix(n) {
			actual[id] = n
		}
	}

	for _, id := range sortedIDs(desired) {
		if _, ok := current[id]; ok {
			continue
		}
		did, err := s.run(ctx, user, pending, "AddMember",
			fmt.Sprintf("add member %q to group %q", userID, desired[id]),
			func() error { return r.LLDAP.AddUserToGroup(ctx, userID, id) })
		if err != nil {
			return syncResult{}, err
		}
		if did {
			actual[id] = desired[id]
		}
	}
	for _, id := range sortedIDs(actual) {
		if _, ok := desired[id]; ok {
			continue
		}
		name := actual[id]
		did, err := s.run(ctx, user, pending, "RemoveMember",
			fmt.Sprintf("remove member %q from group %q", userID, name),
			func() error { return r.LLDAP.RemoveUserFromGroup(ctx, userID, id) })
		if err != nil {
			return syncResult{}, err
		}
		if did {
			delete(actual, id)
		}
	}

	user.Status.Groups = sortedNames(actual)

	switch {
	case len(unknown) > 0:
		slices.Sort(unknown)
		return syncResult{reason: lldapv1alpha1.ReasonUnknownGroup,
			msg: fmt.Sprintf("no LLDAPGroup manages: %s (not touched)", strings.Join(unknown, ", "))}, nil
	case len(notYet) > 0:
		slices.Sort(notYet)
		return syncResult{reason: ReasonGroupPending,
			msg: fmt.Sprintf("groups not created in lldap yet: %s", strings.Join(notYet, ", "))}, nil
	}
	return syncResult{ready: true, reason: lldapv1alpha1.ReasonReconciled, msg: "user matches spec"}, nil
}

func sortedIDs(m map[int64]string) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func sortedNames(m map[int64]string) []string {
	names := make([]string, 0, len(m))
	for _, n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return nil
	}
	return names
}

func (r *LLDAPUserReconciler) finalize(ctx context.Context, s *shared, user *lldapv1alpha1.LLDAPUser) error {
	if !controllerutil.ContainsFinalizer(user, Finalizer) {
		return nil
	}
	name := user.Status.UserID
	if name == "" {
		name = user.EffectiveUsername()
	}
	protected := r.ServiceUser != "" && strings.EqualFold(name, r.ServiceUser)
	if user.EffectiveDeletionPolicy() == lldapv1alpha1.DeletionPolicyDelete && !protected &&
		user.Status.UserID != "" {
		if _, err := r.LLDAP.GetUser(ctx, name); err == nil {
			pending := 0
			if _, err := s.run(ctx, user, &pending, "Delete", fmt.Sprintf("delete user %q", name),
				func() error { return r.LLDAP.DeleteUser(ctx, name) }); err != nil {
				return err
			}
		} else if !errors.Is(err, lldap.ErrNotFound) {
			return err
		}
	} else if protected {
		logf.FromContext(ctx).Info("refusing to delete the operator's own lldap user", "user", name)
	}
	controllerutil.RemoveFinalizer(user, Finalizer)
	return client.IgnoreNotFound(r.Update(ctx, user))
}

// groupToUsers maps an LLDAPGroup to the users that list it.
func (r *LLDAPUserReconciler) groupToUsers(ctx context.Context, obj client.Object) []reconcile.Request {
	group, ok := obj.(*lldapv1alpha1.LLDAPGroup)
	if !ok {
		return nil
	}
	name := group.EffectiveDisplayName()
	var users lldapv1alpha1.LLDAPUserList
	if err := r.List(ctx, &users); err != nil {
		logf.FromContext(ctx).Error(err, "listing users for group event")
		return nil
	}
	var reqs []reconcile.Request
	for i := range users.Items {
		for _, g := range users.Items[i].Spec.Groups {
			if strings.EqualFold(g, name) {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
					Namespace: users.Items[i].Namespace, Name: users.Items[i].Name,
				}})
				break
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLDAPUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lldapv1alpha1.LLDAPUser{}).
		Watches(&lldapv1alpha1.LLDAPGroup{}, handler.EnqueueRequestsFromMapFunc(r.groupToUsers)).
		Named("lldapuser").
		Complete(r)
}
