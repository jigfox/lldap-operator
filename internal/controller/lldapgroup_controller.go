package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	"github.com/jigfox/lldap-operator/internal/lldap"
)

// LLDAPGroupReconciler reconciles a LLDAPGroup object
type LLDAPGroupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	LLDAP    lldap.Client
	Recorder events.EventRecorder
	DryRun   bool
	Interval time.Duration
}

// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *LLDAPGroupReconciler) shared() *shared {
	return &shared{LLDAP: r.LLDAP, Recorder: r.Recorder, DryRun: r.DryRun, Interval: r.Interval}
}

// Reconcile makes the lldap group match the LLDAPGroup.
func (r *LLDAPGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var group lldapv1alpha1.LLDAPGroup
	if err := r.Get(ctx, req.NamespacedName, &group); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	s := r.shared()

	if !group.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, s, &group)
	}
	if controllerutil.AddFinalizer(&group, Finalizer) {
		if err := r.Update(ctx, &group); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := group.DeepCopy()
	pending := 0
	ready, reason, msg, err := r.sync(ctx, s, &group, &pending)
	group.Status.ObservedGeneration = group.Generation
	if err != nil {
		reason, msg = lldapv1alpha1.ReasonLLDAPError, err.Error()
	}
	s.setConditions(&group.Status.Conditions, group.Generation, ready, reason, msg, pending)
	if perr := r.Status().Patch(ctx, &group, client.MergeFrom(orig)); perr != nil {
		if apierrors.IsNotFound(perr) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, perr
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready && reason == lldapv1alpha1.ReasonInvalid {
		// Only a spec change helps; it triggers a new reconcile.
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: s.interval()}, nil
}

// sync creates, adopts, renames. It fills group.Status.
func (r *LLDAPGroupReconciler) sync(ctx context.Context, s *shared, group *lldapv1alpha1.LLDAPGroup,
	pending *int) (ready bool, reason, msg string, err error) {
	name := group.EffectiveDisplayName()
	if hasReservedPrefix(name) {
		return false, lldapv1alpha1.ReasonInvalid,
			fmt.Sprintf("group name %q is reserved for lldap's built-in groups", name), nil
	}
	groups, err := r.LLDAP.ListGroups(ctx)
	if err != nil {
		return false, "", "", err
	}

	var existing *lldap.Group
	if group.Status.GroupID != 0 {
		existing = findGroupByID(groups, group.Status.GroupID)
	}
	adopting := false
	if existing == nil {
		if existing = findGroupByName(groups, name); existing != nil && group.Status.GroupID == 0 {
			adopting = true
		}
	}

	switch {
	case existing == nil:
		var created lldap.Group
		did, err := s.run(ctx, group, pending, "Create", fmt.Sprintf("create group %q", name), func() error {
			var cerr error
			created, cerr = r.LLDAP.CreateGroup(ctx, name)
			return cerr
		})
		if err != nil {
			return false, "", "", err
		}
		if did {
			group.Status.GroupID, group.Status.DisplayName = created.ID, created.DisplayName
		} else {
			group.Status.GroupID = 0
		}
	default:
		if hasReservedPrefix(existing.DisplayName) {
			return false, lldapv1alpha1.ReasonInvalid,
				fmt.Sprintf("lldap group %q is built in and cannot be managed", existing.DisplayName), nil
		}
		if adopting {
			group.Status.Adopted = true
			s.Recorder.Eventf(group, nil, "Normal", "Adopted", "Adopt", "Adopted existing lldap group %q", existing.DisplayName)
		}
		group.Status.GroupID = existing.ID
		group.Status.DisplayName = existing.DisplayName
		if !strings.EqualFold(existing.DisplayName, name) {
			if other := findGroupByName(groups, name); other != nil && other.ID != existing.ID {
				return false, lldapv1alpha1.ReasonInvalid,
					fmt.Sprintf("cannot rename to %q: another lldap group has that name", name), nil
			}
			did, err := s.run(ctx, group, pending, "Rename",
				fmt.Sprintf("rename group %q to %q", existing.DisplayName, name), func() error {
					return r.LLDAP.UpdateGroup(ctx, existing.ID, name)
				})
			if err != nil {
				return false, "", "", err
			}
			if did {
				group.Status.DisplayName = name
			}
		}
	}
	return true, lldapv1alpha1.ReasonReconciled, "group matches spec", nil
}

func (r *LLDAPGroupReconciler) finalize(ctx context.Context, s *shared, group *lldapv1alpha1.LLDAPGroup) error {
	if !controllerutil.ContainsFinalizer(group, Finalizer) {
		return nil
	}
	if group.EffectiveDeletionPolicy() == lldapv1alpha1.DeletionPolicyDelete && group.Status.GroupID != 0 {
		groups, err := r.LLDAP.ListGroups(ctx)
		if err != nil {
			return err
		}
		// Look up by id: only a group we know, and never a built-in one.
		if g := findGroupByID(groups, group.Status.GroupID); g != nil {
			if hasReservedPrefix(g.DisplayName) {
				logf.FromContext(ctx).Info("refusing to delete built-in lldap group", "group", g.DisplayName)
			} else {
				pending := 0
				if _, err := s.run(ctx, group, &pending, "Delete", fmt.Sprintf("delete group %q", g.DisplayName),
					func() error { return r.LLDAP.DeleteGroup(ctx, g.ID) }); err != nil {
					return err
				}
			}
		}
	}
	controllerutil.RemoveFinalizer(group, Finalizer)
	return client.IgnoreNotFound(r.Update(ctx, group))
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLDAPGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lldapv1alpha1.LLDAPGroup{}).
		Named("lldapgroup").
		Complete(r)
}
