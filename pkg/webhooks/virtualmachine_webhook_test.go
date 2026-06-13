package webhooks

import (
	. "github.com/onsi/ginkgo/v2"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

var _ = Describe("VirtualMachine Webhook", func() {

	Context("When creating VirtualMachine under Validating Webhook", func() {
		It("Should deny if required fields are not provided", func() {
			// TODO(user): Add your logic here
		})
	})

	Context("When creating VirtualMachine under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {
			// TODO(user): Add your logic here
		})
	})

	var (
		validator VirtualMachineCustomValidator
		defaulter VirtualMachineCustomDefaulter
	)

	BeforeEach(func() {
		validator = VirtualMachineCustomValidator{}
		defaulter = VirtualMachineCustomDefaulter{}
		// TODO (user): Add any setup logic common to all tests
		_ = validator
		_ = defaulter
		_ = types.VirtualMachine{}
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating VirtualMachine under Conversion Webhook", func() {
		It("Should get the converted version of VirtualMachine", func() {

			// TODO(user): Add your logic here

		})
	})

})
