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

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// nolint:unused
// log is for logging in this package.
var virtualdisksnapshotlog = logf.Log.WithName("virtualdisksnapshot-resource")

// SetupVirtualDiskSnapshotWebhookWithManager registers the webhook for VirtualDiskSnapshot in the manager.
func SetupVirtualDiskSnapshotWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&types.VirtualDiskSnapshot{}).
		WithValidator(&VirtualDiskSnapshotCustomValidator{}).
		WithDefaulter(&VirtualDiskSnapshotCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualdisksnapshot,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots,verbs=create;update,versions=v1beta1,name=mvirtualdisksnapshot-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskSnapshotCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualDiskSnapshot when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskSnapshotCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VirtualDiskSnapshotCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VirtualDiskSnapshot.
func (d *VirtualDiskSnapshotCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	virtualdisksnapshot, ok := obj.(*types.VirtualDiskSnapshot)

	if !ok {
		return fmt.Errorf("expected an VirtualDiskSnapshot object but got %T", obj)
	}
	virtualdisksnapshotlog.Info("Defaulting for VirtualDiskSnapshot", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualdisksnapshot,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots,verbs=create;update,versions=v1beta1,name=vvirtualdisksnapshot-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualDiskSnapshotCustomValidator struct is responsible for validating the VirtualDiskSnapshot resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualDiskSnapshotCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VirtualDiskSnapshotCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualdisksnapshot, ok := obj.(*types.VirtualDiskSnapshot)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDiskSnapshot object but got %T", obj)
	}
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon creation", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	virtualdisksnapshot, ok := newObj.(*types.VirtualDiskSnapshot)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDiskSnapshot object for the newObj but got %T", newObj)
	}
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon update", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VirtualDiskSnapshot.
func (v *VirtualDiskSnapshotCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualdisksnapshot, ok := obj.(*types.VirtualDiskSnapshot)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualDiskSnapshot object but got %T", obj)
	}
	virtualdisksnapshotlog.Info("Validation for VirtualDiskSnapshot upon deletion", "name", virtualdisksnapshot.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
