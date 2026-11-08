package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
)

var _ = Describe("LLDAPUser", func() {
	key := func(n string) types.NamespacedName { return types.NamespacedName{Namespace: "default", Name: n} }

	It("creates the user, follows its group, and deletes it again", func() {
		// The user is created first and lists a group whose LLDAPGroup comes later.
		u := &lldapv1alpha1.LLDAPUser{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "envtest-user"},
			Spec: lldapv1alpha1.LLDAPUserSpec{
				Email: "envtest@example.com", DisplayName: "Env Test", Groups: []string{"envtest-team"},
			},
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())

		Eventually(func(gm Gomega) {
			var got lldapv1alpha1.LLDAPUser
			gm.Expect(k8sClient.Get(ctx, key("envtest-user"), &got)).To(Succeed())
			c := meta.FindStatusCondition(got.Status.Conditions, lldapv1alpha1.ConditionReady)
			gm.Expect(c).NotTo(BeNil())
			gm.Expect(c.Reason).To(Equal(lldapv1alpha1.ReasonUnknownGroup))
		}, timeout, interval).Should(Succeed())
		Expect(fakeLLDAP.User("envtest-user")).NotTo(BeNil())
		Expect(fakeLLDAP.User("envtest-user").Groups).To(BeEmpty())

		g := &lldapv1alpha1.LLDAPGroup{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "envtest-team"}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		// The watch on LLDAPGroup re-queues the user, no waiting for the resync.
		Eventually(func(gm Gomega) {
			var got lldapv1alpha1.LLDAPUser
			gm.Expect(k8sClient.Get(ctx, key("envtest-user"), &got)).To(Succeed())
			gm.Expect(meta.IsStatusConditionTrue(got.Status.Conditions, lldapv1alpha1.ConditionReady)).To(BeTrue())
			gm.Expect(got.Status.Groups).To(ConsistOf("envtest-team"))
			gm.Expect(got.Status.UserID).To(Equal("envtest-user"))
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Delete(ctx, u)).To(Succeed())
		Eventually(func() bool {
			return k8sClient.Get(ctx, key("envtest-user"), &lldapv1alpha1.LLDAPUser{}) != nil
		}, timeout, interval).Should(BeTrue())
		Expect(fakeLLDAP.User("envtest-user")).To(BeNil())
		Expect(k8sClient.Delete(ctx, g)).To(Succeed())
	})

	It("makes the username immutable", func() {
		u := &lldapv1alpha1.LLDAPUser{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "immutable"},
			Spec:       lldapv1alpha1.LLDAPUserSpec{Username: "first", Email: "i@example.com", DeletionPolicy: "Orphan"},
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		u.Spec.Username = "second"
		err := k8sClient.Update(ctx, u)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
		Expect(k8sClient.Delete(ctx, u)).To(Succeed())
	})

	It("requires an email", func() {
		u := &lldapv1alpha1.LLDAPUser{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "noemail"}}
		Expect(k8sClient.Create(ctx, u)).NotTo(Succeed())
	})
})
