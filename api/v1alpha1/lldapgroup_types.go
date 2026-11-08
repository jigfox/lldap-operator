package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeletionPolicy decides what happens to the lldap object when its custom
// resource is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete removes the object from lldap.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan leaves the object in lldap.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// Condition types and reasons shared by both kinds.
const (
	// ConditionReady is True when the lldap object matches the spec.
	ConditionReady = "Ready"
	// ConditionDryRun is True while the operator runs with --dry-run.
	ConditionDryRun = "DryRun"
	// ConditionInvitationSent is set on users once an invitation mail is due:
	// False while it is pending or failed, True once it was sent.
	ConditionInvitationSent = "InvitationSent"

	ReasonReconciled     = "Reconciled"
	ReasonUnknownGroup   = "UnknownGroup"
	ReasonLLDAPError     = "LLDAPError"
	ReasonInvalid        = "Invalid"
	ReasonDryRun         = "DryRun"
	ReasonDryRunPending  = "DryRunPending"
	ReasonDeletionFailed = "DeletionFailed"

	ReasonInvitationPending = "InvitationPending"
	ReasonInvitationFailed  = "InvitationFailed"
	ReasonInvitationSent    = "InvitationSent"
)

// LLDAPGroupSpec defines the desired state of LLDAPGroup.
type LLDAPGroupSpec struct {
	// displayName is the name of the group in lldap. Defaults to metadata.name
	// (applied by the operator). Changing it renames the lldap group. Names
	// starting with "lldap_" belong to lldap's built-in groups and are refused.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.lowerAscii().startsWith('lldap_')",message="names starting with lldap_ are reserved for lldap's built-in groups"
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// deletionPolicy decides whether deleting this resource also deletes the
	// group in lldap (Delete, the default) or leaves it (Orphan).
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// LLDAPGroupStatus defines the observed state of LLDAPGroup.
type LLDAPGroupStatus struct {
	// groupID is the numeric id of the group in lldap.
	// +optional
	GroupID int64 `json:"groupID,omitempty"`

	// displayName is the name the group currently has in lldap.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// adopted is true if the group already existed in lldap when the operator
	// first reconciled this resource.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// observedGeneration is the generation the conditions were computed for.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions: Ready, and DryRun while the operator runs with --dry-run.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=`.status.displayName`
// +kubebuilder:printcolumn:name="Group ID",type=integer,JSONPath=`.status.groupID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Adopted",type=boolean,JSONPath=`.status.adopted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LLDAPGroup manages one group in lldap.
type LLDAPGroup struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec LLDAPGroupSpec `json:"spec"`

	// +optional
	Status LLDAPGroupStatus `json:"status,omitzero"`
}

// EffectiveDisplayName is spec.displayName, or metadata.name if unset.
func (g *LLDAPGroup) EffectiveDisplayName() string {
	if g.Spec.DisplayName != "" {
		return g.Spec.DisplayName
	}
	return g.Name
}

// EffectiveDeletionPolicy is spec.deletionPolicy, or Delete if unset.
func (g *LLDAPGroup) EffectiveDeletionPolicy() DeletionPolicy {
	if g.Spec.DeletionPolicy == "" {
		return DeletionPolicyDelete
	}
	return g.Spec.DeletionPolicy
}

// +kubebuilder:object:root=true

// LLDAPGroupList contains a list of LLDAPGroup.
type LLDAPGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []LLDAPGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &LLDAPGroup{}, &LLDAPGroupList{})
		return nil
	})
}
