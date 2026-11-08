package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
)

// LLDAPGroupReconciler reconciles a LLDAPGroup object
type LLDAPGroupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lldap.jigfox.github.io,resources=lldapgroups/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the LLDAPGroup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *LLDAPGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	// TODO(user): your logic here

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLDAPGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lldapv1alpha1.LLDAPGroup{}).
		Named("lldapgroup").
		Complete(r)
}
