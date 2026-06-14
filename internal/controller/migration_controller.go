package controller

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachinemigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/status,verbs=get;update;patch
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

	if !reflect.DeepEqual(vmm.Status, *status) {
		if err := r.Status().Update(ctx, &vmm); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("update VMM status: %s", err)
		}
	}

	return ctrl.Result{}, nil
}

// reconcile drives a VirtualMachineMigration. The heavy lifting (target Pod
// creation, cloud-hypervisor send/receive) is performed by the VirtualMachine
// controller and the node daemon, both of which key off
// VirtualMachine.Status.Migration. This controller's job is to:
//
//  1. validate the migration is allowed (guard rails),
//  2. stamp VirtualMachine.Status.Migration to kick off that pipeline,
//  3. mirror the pipeline's progress back into the VMM status, and
//  4. on terminal states, clear VirtualMachine.Status.Migration so the VM is
//     eligible for future migrations (clear-on-success/failure).
func (r *VMMReconciler) reconcile(ctx context.Context, vmm *v1beta1.VirtualMachineMigration) error {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling VirtualMachineMigration", "vmm", vmm.Name)

	// Terminal VMM states: nothing left to do.
	if vmm.Status.Phase == v1beta1.VirtualMachineMigrationSucceeded ||
		vmm.Status.Phase == v1beta1.VirtualMachineMigrationFailed {
		return nil
	}

	// Resolve the source VM.
	var vm v1beta1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Name: vmm.Spec.VMName, Namespace: vmm.Namespace}, &vm); err != nil {
		if apierrors.IsNotFound(err) {
			return r.fail(vmm, "VMNotFound", fmt.Sprintf("source VM %q not found", vmm.Spec.VMName))
		}
		return fmt.Errorf("get source VM: %s", err)
	}

	if vmm.Status.SourceNodeName == "" {
		vmm.Status.SourceNodeName = vm.Status.NodeName
	}

	// A migration is already stamped on the VM.
	if vm.Status.Migration != nil {
		// Guard rail: only one VMM may own a VM's migration at a time.
		if vm.Status.Migration.UID != vmm.UID {
			return r.fail(vmm, "MigrationInProgress",
				fmt.Sprintf("VM %q already has an in-progress migration", vm.Name))
		}
		// It's ours: mirror progress back into the VMM status.
		return r.mirror(ctx, vmm, &vm)
	}

	// No migration stamped yet — run guard rails before kicking one off.

	// Guard rail: the VM must be Running to be live-migrated. Stay Pending and
	// wait for a VM change to re-trigger us.
	if vm.Status.Phase != v1beta1.VirtualMachineRunning {
		vmm.Status.Phase = v1beta1.VirtualMachineMigrationPending
		log.Info("source VM not Running yet, waiting", "vm", vm.Name, "phase", vm.Status.Phase)
		return nil
	}

	// Guard rail: respect the Migratable condition computed by the VM
	// controller (dedicated CPU, SR-IOV / bridged / vhost-user interfaces,
	// containerDisk / containerRootfs volumes, fileSystems, ...).
	cond := meta.FindStatusCondition(vm.Status.Conditions, string(v1beta1.VirtualMachineMigratable))
	if cond == nil {
		vmm.Status.Phase = v1beta1.VirtualMachineMigrationPending
		log.Info("Migratable condition not yet known, waiting", "vm", vm.Name)
		return nil
	}
	if cond.Status != metav1.ConditionTrue {
		return r.fail(vmm, "NotMigratable", cond.Message)
	}

	// Kick off the pipeline by stamping the VM's migration status. The VM
	// controller will pick this up and create the target Pod.
	vm.Status.Migration = &v1beta1.VirtualMachineStatusMigration{
		UID:   vmm.UID,
		Phase: v1beta1.VirtualMachineMigrationPending,
	}
	if err := r.Status().Update(ctx, &vm); err != nil {
		if apierrors.IsConflict(err) {
			// The VM controller updated the VM concurrently; we'll be
			// re-triggered by the VM watch and retry the stamp.
			return nil
		}
		return fmt.Errorf("stamp VM migration: %s", err)
	}

	vmm.Status.Phase = v1beta1.VirtualMachineMigrationPending
	r.Recorder.Eventf(vmm, corev1.EventTypeNormal, "MigrationStarted",
		"Started migration of VM %q from node %q", vm.Name, vm.Status.NodeName)
	return nil
}

// mirror copies the in-flight migration state from the VM into the VMM status,
// and on terminal phases clears the VM's migration stamp.
func (r *VMMReconciler) mirror(ctx context.Context, vmm *v1beta1.VirtualMachineMigration, vm *v1beta1.VirtualMachine) error {
	m := vm.Status.Migration
	vmm.Status.Phase = m.Phase
	if m.TargetNodeName != "" {
		vmm.Status.TargetNodeName = m.TargetNodeName
	}

	switch m.Phase {
	case v1beta1.VirtualMachineMigrationSucceeded:
		// By now the source has been cleaned up and vm.Status.NodeName already
		// points at the target, so clearing the stamp is safe.
		r.Recorder.Eventf(vmm, corev1.EventTypeNormal, "MigrationSucceeded",
			"VM %q migrated to node %q", vm.Name, m.TargetNodeName)
		return r.clearVMMigration(ctx, vm)
	case v1beta1.VirtualMachineMigrationFailed:
		return r.handleFailedMigration(ctx, vmm, vm)
	}
	return nil
}

// handleFailedMigration recovers from a failed migration. Whether it is safe to
// reclaim the target depends on the point of no return (PONR) — the moment the
// source VM is torn down on cutover:
//
//   - Before the PONR the source VM is still running and the target Pod is a
//     safe-to-delete orphan, so we reclaim it and leave the VM on the source.
//   - After the PONR the source is gone and the target Pod may hold the only
//     live copy of the VM, so we must NOT delete it; we surface the failure
//     loudly for manual recovery instead.
//
// We detect which side of the PONR we are on from the source VM Pod: it is
// still Running/Pending before cutover, and Succeeded/absent after it.
func (r *VMMReconciler) handleFailedMigration(ctx context.Context, vmm *v1beta1.VirtualMachineMigration, vm *v1beta1.VirtualMachine) error {
	targetPodName := vm.Status.Migration.TargetVMPodName

	sourceAlive := false
	if vm.Status.VMPodName != "" {
		var srcPod corev1.Pod
		err := r.Get(ctx, types.NamespacedName{Name: vm.Status.VMPodName, Namespace: vm.Namespace}, &srcPod)
		switch {
		case err == nil:
			sourceAlive = srcPod.Status.Phase == corev1.PodRunning || srcPod.Status.Phase == corev1.PodPending
		case apierrors.IsNotFound(err):
			sourceAlive = false
		default:
			return fmt.Errorf("get source VM pod: %s", err)
		}
	}

	if sourceAlive {
		// Safe abort: delete the orphaned target VM Pod (its hotplug volume
		// Pods cascade via owner reference) and keep the VM on the source.
		if targetPodName != "" {
			if err := r.deletePodIfExists(ctx, vm.Namespace, targetPodName); err != nil {
				return err
			}
		}
		r.Recorder.Eventf(vmm, corev1.EventTypeWarning, "MigrationFailed",
			"Migration of VM %q failed before cutover; VM remains on node %q, target Pod %q reclaimed",
			vm.Name, vm.Status.NodeName, targetPodName)
	} else {
		// Past the PONR: the source is gone and the target may hold the only
		// live VM state. Leave the target Pod in place for manual recovery.
		r.Recorder.Eventf(vmm, corev1.EventTypeWarning, "MigrationFailedNeedsRecovery",
			"Migration of VM %q failed after cutover; source is gone, target Pod %q left in place for manual recovery on node %q",
			vm.Name, targetPodName, vm.Status.Migration.TargetNodeName)
	}

	return r.clearVMMigration(ctx, vm)
}

// deletePodIfExists deletes a Pod by name, treating an already-absent Pod as
// success.
func (r *VMMReconciler) deletePodIfExists(ctx context.Context, namespace, name string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete target VM Pod %q: %s", name, err)
	}
	return nil
}

// clearVMMigration removes the migration stamp from the VM so it becomes
// eligible for future migrations.
func (r *VMMReconciler) clearVMMigration(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	vm.Status.Migration = nil
	if err := r.Status().Update(ctx, vm); err != nil {
		if apierrors.IsConflict(err) {
			return nil
		}
		return fmt.Errorf("clear VM migration: %s", err)
	}
	return nil
}

// fail records a terminal failure on the VMM. It returns nil so the status
// update is persisted by the caller rather than triggering a requeue.
func (r *VMMReconciler) fail(vmm *v1beta1.VirtualMachineMigration, reason, msg string) error {
	vmm.Status.Phase = v1beta1.VirtualMachineMigrationFailed
	r.Recorder.Eventf(vmm, corev1.EventTypeWarning, reason, "%s", msg)
	return nil
}

func (r *VMMReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachineMigration{}).
		Watches(&v1beta1.VirtualMachine{}, handler.EnqueueRequestsFromMapFunc(r.vmmsForVM)).
		Complete(r)
}

// vmmsForVM maps a VirtualMachine event to the non-terminal
// VirtualMachineMigrations that reference it, so migration progress on the VM
// re-triggers reconciliation of the owning VMM.
func (r *VMMReconciler) vmmsForVM(ctx context.Context, obj client.Object) []reconcile.Request {
	vm, ok := obj.(*v1beta1.VirtualMachine)
	if !ok {
		return nil
	}

	var vmmList v1beta1.VirtualMachineMigrationList
	if err := r.List(ctx, &vmmList, client.InNamespace(vm.Namespace)); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for i := range vmmList.Items {
		vmm := &vmmList.Items[i]
		if vmm.Spec.VMName != vm.Name {
			continue
		}
		if vmm.Status.Phase == v1beta1.VirtualMachineMigrationSucceeded ||
			vmm.Status.Phase == v1beta1.VirtualMachineMigrationFailed {
			continue
		}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: vmm.Name, Namespace: vmm.Namespace},
		})
	}
	return reqs
}
