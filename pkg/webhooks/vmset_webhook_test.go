package webhooks

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	k8sResource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("VMSet Webhook", func() {

	Context("When creating VMSet under Validating Webhook", func() {
		It("Should admit if all required fields are provided", func() {
			By("By creating a new VMSet")
			ctx := context.Background()
			vmset := &types.VMSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmset",
					Namespace: "default",
				},
				Spec: types.VMSetSpec{
					Replicas: &[]int32{1}[0],
					Selector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "test",
						},
					},
					Template: types.VirtualMachineTemplateSpec{
						Metadata: types.TemplateMetadata{
							Labels: map[string]string{
								"app": "test",
							},
						},
						Spec: types.VirtualMachineSpec{
							RunPolicy: types.RunPolicyOnce,
							Instance: types.Instance{
								CPU: types.CPU{
									Sockets:        1,
									CoresPerSocket: 1,
								},
								Memory: types.Memory{
									Size: k8sResource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmset)).Should(Succeed())
		})
	})

	Context("When creating VMSet under Validating Webhook", func() {
		It("Should deny if required fields are not provided", func() {
			By("By creating a new VMSet")
			ctx := context.Background()
			vmset := &types.VMSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmset-invalid",
					Namespace: "default",
				},
				Spec: types.VMSetSpec{
					Replicas: &[]int32{1}[0],
					// Missing Selector - this should cause validation to fail
					Template: types.VirtualMachineTemplateSpec{
						Metadata: types.TemplateMetadata{
							Labels: map[string]string{
								"app": "test",
							},
						},
						Spec: types.VirtualMachineSpec{
							RunPolicy: types.RunPolicyOnce,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmset)).ShouldNot(Succeed())
		})
	})

	Context("When creating VMSet under Defaulting Webhook", func() {
		// TODO (user): Add logic for defaulting webhooks
		// Example:
		// It("Should apply defaults when a required field is empty", func() {
		//     By("simulating a scenario where defaults should be applied")
		//     obj.SomeFieldWithDefault = ""
		//     By("calling the Default method to apply defaults")
		//     defaulter.Default(ctx, obj)
		//     By("checking that the default values are set")
		//     Expect(obj.SomeFieldWithDefault).To(Equal("default_value"))
		// })
	})

	Context("When creating or updating VMSet under Validating Webhook", func() {
		// TODO (user): Add logic for validating webhooks
		// Example:
		// It("Should deny creation if a required field is missing", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = ""
		//     Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
		// })
		//
		// It("Should admit creation if all required fields are present", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = "valid_value"
		//     Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
		// })
		//
		// It("Should validate updates correctly", func() {
		//     By("simulating a valid update scenario")
		//     oldObj.SomeRequiredField = "updated_value"
		//     obj.SomeRequiredField = "updated_value"
		//     Expect(validator.ValidateUpdate(ctx, oldObj, obj)).To(BeNil())
		// })
	})

})
