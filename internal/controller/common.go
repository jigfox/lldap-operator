package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
	"github.com/jigfox/lldap-operator/internal/lldap"
)

const (
	// Finalizer is set on every managed resource so lldap is cleaned up first.
	Finalizer = "lldap.jigfox.github.io/finalizer"

	// DefaultInterval is how often a resource is re-checked for drift.
	DefaultInterval = 10 * time.Minute

	// pendingRetry is how soon a user is retried while a group it lists has an
	// LLDAPGroup that does not exist in lldap yet.
	pendingRetry = 15 * time.Second

	reasonDryRun = "DryRun"
)

// shared holds what both reconcilers need.
type shared struct {
	LLDAP    lldap.Client
	Recorder events.EventRecorder
	// DryRun computes and reports every change but sends no mutation to lldap.
	DryRun bool
	// Interval is the periodic resync used to correct drift made in lldap.
	Interval time.Duration
}

func (s *shared) interval() time.Duration {
	if s.Interval <= 0 {
		return DefaultInterval
	}
	return s.Interval
}

// run performs one mutation against lldap, or in dry-run mode only logs it and
// emits an Event. It returns whether the mutation was really sent; skipped
// mutations are counted in pending.
func (s *shared) run(ctx context.Context, obj client.Object, pending *int, reason, msg string,
	fn func() error) (bool, error) {
	log := logf.FromContext(ctx)
	if s.DryRun {
		*pending++
		log.Info("dry-run: would "+msg, "action", reason)
		s.Recorder.Eventf(obj, nil, corev1.EventTypeNormal, reasonDryRun, reason, "DryRun: would %s", msg)
		return false, nil
	}
	if err := fn(); err != nil {
		return false, fmt.Errorf("%s: %w", msg, err)
	}
	log.Info(msg, "action", reason)
	s.Recorder.Eventf(obj, nil, corev1.EventTypeNormal, reason, reason, "%s", capitalize(msg))
	return true, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// setConditions writes Ready and (in dry-run) DryRun on the conditions slice.
func (s *shared) setConditions(conds *[]metav1.Condition, generation int64, ready bool,
	reason, msg string, pending int) {
	if s.DryRun && pending > 0 {
		ready, reason = false, lldapv1alpha1.ReasonDryRunPending
		msg = fmt.Sprintf("dry-run: %d change(s) not applied", pending)
	}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: lldapv1alpha1.ConditionReady, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: generation,
	})
	if !s.DryRun {
		meta.RemoveStatusCondition(conds, lldapv1alpha1.ConditionDryRun)
		return
	}
	dryMsg := "dry-run: no changes needed"
	if pending > 0 {
		dryMsg = fmt.Sprintf("dry-run: %d change(s) would be made", pending)
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: lldapv1alpha1.ConditionDryRun, Status: metav1.ConditionTrue, Reason: lldapv1alpha1.ReasonDryRun,
		Message: dryMsg, ObservedGeneration: generation,
	})
}

func hasReservedPrefix(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "lldap_")
}

func findGroupByName(groups []lldap.Group, name string) *lldap.Group {
	for i := range groups {
		if strings.EqualFold(groups[i].DisplayName, name) {
			return &groups[i]
		}
	}
	return nil
}

func findGroupByID(groups []lldap.Group, id int64) *lldap.Group {
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i]
		}
	}
	return nil
}
