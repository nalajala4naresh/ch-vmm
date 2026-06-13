package webhooks

import (
	. "github.com/onsi/ginkgo/v2"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

var _ = Describe("VirtualMachineMigration Webhook", func() {

	Context("When creating VirtualMachineMigration under Validating Webhook", func() {
		It("Should deny if required fields are not provided", func() {
			// TODO(user): Add your logic here
		})
	})

	Context("When creating VirtualMachineMigration under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {
			// TODO(user): Add your logic here
		})
	})

	var (
		validator VirtualMachineMigrationCustomValidator
		defaulter VirtualMachineMigrationCustomDefaulter
	)

	BeforeEach(func() {
		validator = VirtualMachineMigrationCustomValidator{}
		defaulter = VirtualMachineMigrationCustomDefaulter{}
		// TODO (user): Add any setup logic common to all tests
		_ = validator
		_ = defaulter
		_ = types.VirtualMachineMigration{}
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

})
