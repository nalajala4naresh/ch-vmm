package webhooks

import (
	"context"
	"fmt"
	"reflect"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// nolint:unused
// log is for logging in this package.
var vmpoollog = logf.Log.WithName("vmpool-resource")

// SetupVMPoolWebhookWithManager registers the webhook for VMPool in the manager.
func SetupVMPoolWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&types.VMPool{}).
		WithValidator(&VMPoolCustomValidator{}).
		WithDefaulter(&VMPoolCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-vmpool,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmpools,verbs=create;update,versions=v1beta1,name=mvmpool-v1beta1.kb.io,admissionReviewVersions=v1

// VMPoolCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VMPool when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VMPoolCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VMPoolCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VMPool.
func (d *VMPoolCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	vmpool, ok := obj.(*types.VMPool)
	if vmpool.Namespace == "" {
		vmpool.Namespace = "default"
	}

	if !ok {
		return fmt.Errorf("expected an VMPool object but got %T", obj)
	}
	vmpoollog.Info("Defaulting for VMPool", "name", vmpool.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-vmpool,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=vmpools,verbs=create;update,versions=v1beta1,name=vvmpool-v1beta1.kb.io,admissionReviewVersions=v1

// VMPoolCustomValidator struct is responsible for validating the VMPool resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VMPoolCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VMPoolCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vmpool, ok := obj.(*types.VMPool)
	if !ok {
		return nil, fmt.Errorf("expected a VMPool object but got %T", obj)
	}

	// For CREATE, just validate that selector is not empty
	if len(vmpool.Spec.Selector.MatchLabels) == 0 {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMPool").GroupKind(),
			vmpool.Name,
			field.ErrorList{
				field.Required(field.NewPath("spec").Child("selector").Child("matchLabels"),
					"selector cannot be empty"),
			},
		)
	}

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {

	newPool, ok := newObj.(*types.VMPool)
	if !ok {
		return nil, fmt.Errorf("expected a VMPool object for the newObj but got %T", newObj)
	}
	oldPool, ok := oldObj.(*types.VMPool)
	if !ok {
		return nil, fmt.Errorf("expected a VMPool object for the newObj but got %T", newObj)
	}
	vmsetlog.Info("Validation for VMPool upon update", "name", newPool.GetName())

	if !reflect.DeepEqual(newPool.Spec.Selector, oldPool.Spec.Selector) {
		return nil, apierrors.NewInvalid(
			types.GroupVersion.WithKind("VMPool").GroupKind(),
			newPool.Name,
			field.ErrorList{
				field.Forbidden(field.NewPath("spec").Child("selector"),
					"selector is immutable"),
			},
		)
	}

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VMPool.
func (v *VMPoolCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vmpool, ok := obj.(*types.VMPool)
	if !ok {
		return nil, fmt.Errorf("expected a VMPool object but got %T", obj)
	}
	vmpoollog.Info("Validation for VMPool upon deletion", "name", vmpool.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
