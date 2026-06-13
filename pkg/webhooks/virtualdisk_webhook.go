/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhooks

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cloudhypervisorquilltodayv1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var virtualdisklog = logf.Log.WithName("virtualdisk-resource")

// SetupVirtualDiskWebhookWithManager registers the webhook for VirtualDisk in the manager.
func SetupVirtualDiskWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&cloudhypervisorquilltodayv1beta1.VirtualDisk{}).
		WithValidator(&VirtualDiskCustomValidator{}).
		WithDefaulter(&VirtualDiskCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualdisk,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisks,verbs=create;update,versions=v1beta1,name=mvirtualdisk-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualDisk when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VirtualDiskCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VirtualDisk.
func (d *VirtualDiskCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	virtualdisk, ok := obj.(*cloudhypervisorquilltodayv1beta1.VirtualDisk)

	if !ok {
		return fmt.Errorf("expected an VirtualDisk object but got %T", obj)
	}
	virtualdisklog.Info("Defaulting for VirtualDisk", "name", virtualdisk.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualdisk,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisks,verbs=create;update,versions=v1beta1,name=vvirtualdisk-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskCustomValidator struct is responsible for validating the VirtualDisk resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VirtualDiskCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualdisk, ok := obj.(*cloudhypervisorquilltodayv1beta1.VirtualDisk)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDisk object but got %T", obj)
	}
	virtualdisklog.Info("Validation for VirtualDisk upon creation", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	virtualdisk, ok := newObj.(*cloudhypervisorquilltodayv1beta1.VirtualDisk)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDisk object for the newObj but got %T", newObj)
	}
	virtualdisklog.Info("Validation for VirtualDisk upon update", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VirtualDisk.
func (v *VirtualDiskCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualdisk, ok := obj.(*cloudhypervisorquilltodayv1beta1.VirtualDisk)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDisk object but got %T", obj)
	}
	virtualdisklog.Info("Validation for VirtualDisk upon deletion", "name", virtualdisk.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
