package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// LLDAPUserSpec defines the desired state of LLDAPUser.
type LLDAPUserSpec struct {
	// username is the lldap user id. Defaults to metadata.name (applied by the
	// operator) and cannot be changed once set.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="username is immutable"
	// +optional
	Username string `json:"username,omitempty"`

	// email of the user.
	// +kubebuilder:validation:MinLength=3
	// +required
	Email string `json:"email"`

	// displayName of the user. Left untouched in lldap if empty.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// firstName of the user. Left untouched in lldap if empty.
	// +optional
	FirstName string `json:"firstName,omitempty"`

	// lastName of the user. Left untouched in lldap if empty.
	// +optional
	LastName string `json:"lastName,omitempty"`

	// groups lists display names of lldap groups the user is a member of. Only
	// groups managed by an LLDAPGroup are changed; names without one are
	// reported in the Ready condition and never touched.
	// +listType=set
	// +optional
	Groups []string `json:"groups,omitempty"`

	// deletionPolicy decides whether deleting this resource also deletes the
	// user in lldap (Delete, the default) or leaves it (Orphan).
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// sendInvitation controls the password-reset mail the operator triggers
	// once when it creates this user (never when it adopts one). Unset or true
	// follows the operator's --invite mode, false suppresses the mail for this
	// user. Has no effect when the operator runs with --invite=none.
	// +kubebuilder:default=true
	// +optional
	SendInvitation *bool `json:"sendInvitation,omitempty"`
}

// LLDAPUserStatus defines the observed state of LLDAPUser.
type LLDAPUserStatus struct {
	// userID is the id of the user in lldap (its username).
	// +optional
	UserID string `json:"userID,omitempty"`

	// adopted is true if the user already existed in lldap when the operator
	// first reconciled this resource.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// groups are the managed groups the user is currently a member of.
	// +listType=set
	// +optional
	Groups []string `json:"groups,omitempty"`

	// invitationSentAt is when the operator triggered the invitation mail. It is
	// recorded before the mail is sent, so a user is never invited twice.
	// +optional
	InvitationSentAt *metav1.Time `json:"invitationSentAt,omitempty"`

	// observedGeneration is the generation the conditions were computed for.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions: Ready, InvitationSent once an invitation is due, and DryRun
	// while the operator runs with --dry-run.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.status.userID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Adopted",type=boolean,JSONPath=`.status.adopted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LLDAPUser manages one user in lldap and its group memberships.
type LLDAPUser struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec LLDAPUserSpec `json:"spec"`

	// +optional
	Status LLDAPUserStatus `json:"status,omitzero"`
}

// EffectiveUsername is spec.username, or metadata.name if unset.
func (u *LLDAPUser) EffectiveUsername() string {
	if u.Spec.Username != "" {
		return u.Spec.Username
	}
	return u.Name
}

// EffectiveDeletionPolicy is spec.deletionPolicy, or Delete if unset.
func (u *LLDAPUser) EffectiveDeletionPolicy() DeletionPolicy {
	if u.Spec.DeletionPolicy == "" {
		return DeletionPolicyDelete
	}
	return u.Spec.DeletionPolicy
}

// +kubebuilder:object:root=true

// LLDAPUserList contains a list of LLDAPUser.
type LLDAPUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []LLDAPUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &LLDAPUser{}, &LLDAPUserList{})
		return nil
	})
}
