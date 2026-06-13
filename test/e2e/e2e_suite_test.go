/*
Copyright 2024.

Licensed under the MIT License. See LICENSE file in the project root for full license information.
*/

package e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting virtmanager suite\n")
	RunSpecs(t, "e2e suite")
}
