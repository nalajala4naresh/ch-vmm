package webhooks

import (
	"context"
	"fmt"
	"os"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// nolint:unused
// log is for logging in this package.
var virtualmachinemigrationlog = logf.Log.WithName("virtualmachinemigration-resource")
var c client.Client

func init() {

	cfg, err := config.GetConfig()
	if err != nil {
		os.Exit(1)
	}
	c, err = client.New(cfg, client.Options{})
	if err != nil {
		os.Exit(1)

	}

}

// SetupVirtualMachineMigrationWebhookWithManager registers the webhook for VirtualMachineMigration in the manager.
func SetupVirtualMachineMigrationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&types.VirtualMachineMigration{}).
		WithValidator(&VirtualMachineMigrationCustomValidator{}).
		WithDefaulter(&VirtualMachineMigrationCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualmachinemigration,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations,verbs=create;update,versions=v1beta1,name=mvirtualmachinemigration-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualMachineMigrationCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualMachineMigration when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualMachineMigrationCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VirtualMachineMigrationCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VirtualMachineMigration.
func (d *VirtualMachineMigrationCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	virtualmachinemigration, ok := obj.(*types.VirtualMachineMigration)

	if !ok {
		return fmt.Errorf("expected an VirtualMachineMigration object but got %T", obj)
	}
	virtualmachinemigrationlog.Info("Defaulting for VirtualMachineMigration", "name", virtualmachinemigration.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualmachinemigration,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations,verbs=create;update,versions=v1beta1,name=vvirtualmachinemigration-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualMachineMigrationCustomValidator struct is responsible for validating the VirtualMachineMigration resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualMachineMigrationCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VirtualMachineMigrationCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineMigration.
func (v *VirtualMachineMigrationCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualmachinemigration, ok := obj.(*types.VirtualMachineMigration)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineMigration object but got %T", obj)
	}
	virtualmachinemigrationlog.Info("Validation for VirtualMachineMigration upon creation", "name", virtualmachinemigration.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineMigration.
func (v *VirtualMachineMigrationCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newVirtualMachineMigration, ok := newObj.(*types.VirtualMachineMigration)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineMigration object for the newObj but got %T", newObj)
	}
	oldVirtualMachineMigration, ok := oldObj.(*types.VirtualMachineMigration)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineMigration object for the oldObj but got %T", oldObj)
	}
	virtualmachinemigrationlog.Info("Validation for VirtualMachineMigration upon update", "name", newVirtualMachineMigration.GetName())

	// TODO(user): fill in your validation logic upon object update.
	_ = oldVirtualMachineMigration

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineMigration.
func (v *VirtualMachineMigrationCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualmachinemigration, ok := obj.(*types.VirtualMachineMigration)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineMigration object but got %T", obj)
	}
	virtualmachinemigrationlog.Info("Validation for VirtualMachineMigration upon deletion", "name", virtualmachinemigration.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}

func ValidateVMM(ctx context.Context, vmm *types.VirtualMachineMigration, oldVMM *types.VirtualMachineMigration) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, ValidateVMMSpec(ctx, vmm.Namespace, &vmm.Spec, field.NewPath("spec"))...)
	return errs
}

func ValidateVMMSpec(ctx context.Context, namespace string, spec *types.VirtualMachineMigrationSpec, fieldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	if spec == nil {
		errs = append(errs, field.Required(fieldPath, ""))
		return errs
	}
	errs = append(errs, ValidateVMName(ctx, namespace, spec.VMName, fieldPath.Child("vmName"))...)
	return errs
}

func ValidateVMName(ctx context.Context, namespace string, vmName string, fieldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	if vmName == "" {
		errs = append(errs, field.Required(fieldPath, ""))
		return errs
	}

	vmKey := client.ObjectKey{Namespace: namespace, Name: vmName}
	var vm types.VirtualMachine
	if err := c.Get(ctx, vmKey, &vm); err != nil {
		if apierrors.IsNotFound(err) {
			errs = append(errs, field.NotFound(fieldPath, vmName))
		} else {
			errs = append(errs, field.InternalError(fieldPath, err))
		}
		return errs
	}

	migratableCondition := meta.FindStatusCondition(vm.Status.Conditions, string(types.VirtualMachineMigratable))
	if migratableCondition == nil {
		errs = append(errs, field.Forbidden(fieldPath, "VM migratable condition status is unknown"))
		return errs
	}

	if migratableCondition.Status != metav1.ConditionTrue {
		errs = append(errs, field.Forbidden(fieldPath, migratableCondition.Message))
	}

	return errs
}
