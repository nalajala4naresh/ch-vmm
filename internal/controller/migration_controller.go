package controller

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

type VMMReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile

func (r *VMMReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var vmm v1beta1.VirtualMachineMigration
	if err := r.Get(ctx, req.NamespacedName, &vmm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := vmm.Status.DeepCopy()
	if err := r.reconcile(ctx, &vmm); err != nil {
		r.Recorder.Eventf(&vmm, corev1.EventTypeWarning, "FailedReconcile", "Failed to reconcile VMM: %s", err)
		return ctrl.Result{}, err
	}

	if !reflect.DeepEqual(vmm.Status, status) {
		if err := r.Status().Update(ctx, &vmm); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("update VMM status: %s", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *VMMReconciler) reconcile(ctx context.Context, vmm *v1beta1.VirtualMachineMigration) error {
	log := ctrl.LoggerFrom(ctx)

	// TODO: Add your reconcile logic here
	log.Info("Reconciling VirtualMachineMigration", "vmm", vmm.Name)

	// Get the source VM
	var sourceVM v1beta1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{
		Name:      vmm.Spec.VMName,
		Namespace: vmm.Namespace,
	}, &sourceVM); err != nil {
		return fmt.Errorf("get source VM: %s", err)
	}

	// Set the source node name if not set
	if vmm.Status.SourceNodeName == "" && sourceVM.Status.NodeName != "" {
		vmm.Status.SourceNodeName = sourceVM.Status.NodeName
	}

	// Default migration logic (live migration)
	return r.reconcileMigration(ctx, vmm, &sourceVM)
}

func (r *VMMReconciler) reconcileMigration(ctx context.Context, vmm *v1beta1.VirtualMachineMigration, sourceVM *v1beta1.VirtualMachine) error {
	log := ctrl.LoggerFrom(ctx)

	switch vmm.Status.Phase {
	case "":
		// Initialize migration
		vmm.Status.Phase = v1beta1.VirtualMachineMigrationPending
		log.Info("Initializing migration", "source", sourceVM.Name)

	case v1beta1.VirtualMachineMigrationPending:
		// Start migration process
		vmm.Status.Phase = v1beta1.VirtualMachineMigrationRunning
		log.Info("Starting migration", "source", sourceVM.Name)

	case v1beta1.VirtualMachineMigrationRunning:
		// TODO: Implement actual migration logic
		log.Info("Migration in progress", "source", sourceVM.Name)

	case v1beta1.VirtualMachineMigrationSucceeded, v1beta1.VirtualMachineMigrationFailed:
		// Migration completed, no action needed
		log.Info("Migration completed", "source", sourceVM.Name, "status", vmm.Status.Phase)

	default:
		log.Info("Unknown migration phase", "phase", vmm.Status.Phase)
	}

	return nil
}

func (r *VMMReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachineMigration{}).
		Watches(&v1beta1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			// TODO: Implement mapping logic
			return []reconcile.Request{}
		})).
		Complete(r)
}
