package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	lldapv1alpha1 "github.com/jigfox/lldap-operator/api/v1alpha1"
)

const (
	timeout  = 20 * time.Second
	interval = 200 * time.Millisecond
)

var _ = Describe("LLDAPGroup", func() {
	key := func(n string) types.NamespacedName { return types.NamespacedName{Namespace: "default", Name: n} }

	It("defaults the deletion policy, creates the group and deletes it again", func() {
		g := &lldapv1alpha1.LLDAPGroup{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "envtest-group"}}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		Expect(g.Spec.DeletionPolicy).To(Equal(lldapv1alpha1.DeletionPolicyDelete))

		Eventually(func(gm Gomega) {
			var got lldapv1alpha1.LLDAPGroup
			gm.Expect(k8sClient.Get(ctx, key("envtest-group"), &got)).To(Succeed())
			gm.Expect(meta.IsStatusConditionTrue(got.Status.Conditions, lldapv1alpha1.ConditionReady)).To(BeTrue())
			gm.Expect(got.Status.GroupID).NotTo(BeZero())
		}, timeout, interval).Should(Succeed())
		Expect(fakeLLDAP.GroupNames()).To(ContainElement("envtest-group"))

		Expect(k8sClient.Delete(ctx, g)).To(Succeed())
		Eventually(func() bool {
			return k8sClient.Get(ctx, key("envtest-group"), &lldapv1alpha1.LLDAPGroup{}) != nil
		}, timeout, interval).Should(BeTrue())
		Expect(fakeLLDAP.GroupNames()).NotTo(ContainElement("envtest-group"))
	})

	It("rejects names reserved for lldap's built-in groups", func() {
		for _, name := range []string{"lldap_admin", "LLDAP_Custom"} {
			g := &lldapv1alpha1.LLDAPGroup{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "reserved"},
				Spec:       lldapv1alpha1.LLDAPGroupSpec{DisplayName: name},
			}
			err := k8sClient.Create(ctx, g)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved for lldap"))
		}
	})

	It("rejects an unknown deletion policy", func() {
		g := &lldapv1alpha1.LLDAPGroup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "badpolicy"},
			Spec:       lldapv1alpha1.LLDAPGroupSpec{DeletionPolicy: "Maybe"},
		}
		Expect(k8sClient.Create(ctx, g)).NotTo(Succeed())
	})
})
