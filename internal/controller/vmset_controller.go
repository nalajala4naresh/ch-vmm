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

package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const (
	VMSetProtectionFinalizer = "cloudhypervisor.quill.today/vmset-protection"
)
const VMSET_LABEL = "microbox-api/vmset-id"

// VMSetReconciler reconciles a VMSet object
type VMSetReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VMSet object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *VMSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the VMSet instance
	vmSet := &v1beta1.VMSet{}
	err := r.Get(ctx, req.NamespacedName, vmSet)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("VMSet resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get VMSet")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if vmSet.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, vmSet)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(vmSet, VMSetProtectionFinalizer) {
		controllerutil.AddFinalizer(vmSet, VMSetProtectionFinalizer)
		return ctrl.Result{}, r.Update(ctx, vmSet)
	}

	// Reconcile the VMSet
	return r.reconcileVMSet(ctx, vmSet)
}

func (r *VMSetReconciler) reconcileVMSet(ctx context.Context, vmSet *v1beta1.VMSet) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get current VMs managed by this VMSet
	currentVMs, err := r.getCurrentVMs(ctx, vmSet)
	if err != nil {
		log.Error(err, "Failed to get current VMs")
		return ctrl.Result{}, err
	}

	// Sort VMs by ordinal for consistent ordering
	sort.Slice(currentVMs, func(i, j int) bool {
		return getOrdinal(currentVMs[i].Name) < getOrdinal(currentVMs[j].Name)
	})

	// Calculate desired replicas
	desiredReplicas := int32(0)
	if vmSet.Spec.Replicas != nil {
		desiredReplicas = *vmSet.Spec.Replicas
	}

	currentReplicas := int32(len(currentVMs))

	// Scale up or down as needed
	if currentReplicas < desiredReplicas {
		// Scale up
		return r.scaleUp(ctx, vmSet, currentVMs, desiredReplicas)
	} else if currentReplicas > desiredReplicas {
		// Scale down
		return r.scaleDown(ctx, vmSet, currentVMs, desiredReplicas)
	}

	// Update status
	return r.updateStatus(ctx, vmSet, currentVMs)
}

func (r *VMSetReconciler) scaleUp(ctx context.Context, vmSet *v1beta1.VMSet, currentVMs []v1beta1.VirtualMachine, desiredReplicas int32) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Find the next ordinal to create
	nextOrdinal := int32(len(currentVMs))
	vmName := fmt.Sprintf("%s-%d", vmSet.Name, nextOrdinal)

	// Check if VM already exists to prevent duplicate creation
	existingVM := &v1beta1.VirtualMachine{}
	err := r.Get(ctx, types.NamespacedName{Name: vmName, Namespace: vmSet.Namespace}, existingVM)
	if err == nil {
		// VM already exists, skip creation
		log.Info("VM already exists, skipping creation", "vmName", vmName)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	} else if !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to check VM existence", "vmName", vmName)
		return ctrl.Result{}, err
	}

	// Create VirtualDisks first
	err = r.createVirtualDisks(ctx, vmSet, nextOrdinal)
	if err != nil {
		log.Error(err, "Failed to create VirtualDisks", "ordinal", nextOrdinal)
		return ctrl.Result{}, err
	}

	// Create the VM with VirtualDisk references already injected
	vm := r.constructVM(vmSet, nextOrdinal)
	if err := controllerutil.SetControllerReference(vmSet, vm, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	err = r.Create(ctx, vm)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		log.Error(err, "Failed to create VM", "ordinal", nextOrdinal)
		return ctrl.Result{}, err
	}

	log.Info("Successfully created VM with VirtualDisks", "name", vm.Name, "ordinal", nextOrdinal)
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func (r *VMSetReconciler) shouldDeleteVirtualDisksOnScale(vmSet *v1beta1.VMSet) bool {
	if vmSet.Spec.Strategy.VolumeClaimRetentionPolicy == nil {
		return false // Default to retain
	}
	return vmSet.Spec.Strategy.VolumeClaimRetentionPolicy.WhenScaled == "Delete"
}

func (r *VMSetReconciler) shouldDeleteVirtualDisksOnDelete(vmSet *v1beta1.VMSet) bool {
	if vmSet.Spec.Strategy.VolumeClaimRetentionPolicy == nil {
		return false // Default to retain
	}
	return vmSet.Spec.Strategy.VolumeClaimRetentionPolicy.WhenDeleted == "Delete"
}

func (r *VMSetReconciler) scaleDown(ctx context.Context, vmSet *v1beta1.VMSet, currentVMs []v1beta1.VirtualMachine, desiredReplicas int32) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Delete VMs with highest ordinals first (StatefulSet behavior)
	toDelete := int32(len(currentVMs)) - desiredReplicas

	for i := int32(0); i < toDelete; i++ {
		vmToDelete := currentVMs[len(currentVMs)-1-int(i)]
		ordinal := getOrdinal(vmToDelete.Name)

		// Delete VM first
		err := r.Delete(ctx, &vmToDelete)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete VM", "name", vmToDelete.Name)
			return ctrl.Result{}, err
		}

		// Delete associated VirtualDisks
		if r.shouldDeleteVirtualDisksOnScale(vmSet) {
			err = r.deleteVirtualDisks(ctx, vmSet, ordinal)
			if err != nil {
				log.Error(err, "Failed to delete VirtualDisks", "ordinal", ordinal)
				return ctrl.Result{}, err
			}

			log.Info("Successfully deleted VM and VirtualDisks", "name", vmToDelete.Name, "ordinal", ordinal)
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func (r *VMSetReconciler) createVirtualDisks(ctx context.Context, vmSet *v1beta1.VMSet, ordinal int32) error {
	for _, diskTemplate := range vmSet.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-%d", vmSet.Name, diskTemplate.MetaData.Name, ordinal)

		// Check if VirtualDisk already exists
		existingDisk := &v1beta1.VirtualDisk{}
		err := r.Get(ctx, types.NamespacedName{Name: diskName, Namespace: vmSet.Namespace}, existingDisk)
		if err == nil {
			// VirtualDisk already exists, skip creation
			log.FromContext(ctx).Info("VirtualDisk already exists, skipping creation", "diskName", diskName)
			continue
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to check VirtualDisk existence %s: %w", diskName, err)
		}

		disk := &v1beta1.VirtualDisk{
			ObjectMeta: metav1.ObjectMeta{
				Name:        diskName,
				Namespace:   vmSet.Namespace,
				Labels:      r.getLabelsForOrdinal(vmSet, ordinal),
				Annotations: diskTemplate.MetaData.Annotations,
			},
			Spec: diskTemplate.Spec,
		}

		// Set VMSet as the controller of the VirtualDisk (VM will be created later)
		if err := controllerutil.SetControllerReference(vmSet, disk, r.Scheme); err != nil {
			return fmt.Errorf("failed to set VMSet as controller of VirtualDisk %s: %w", diskName, err)
		}

		err = r.Create(ctx, disk)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create VirtualDisk %s: %w", disk.Name, err)
		}
	}
	return nil
}

func (r *VMSetReconciler) deleteVirtualDisks(ctx context.Context, vmSet *v1beta1.VMSet, ordinal int32) error {
	for _, diskTemplate := range vmSet.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-%d", vmSet.Name, diskTemplate.MetaData.Name, ordinal)

		disk := &v1beta1.VirtualDisk{
			ObjectMeta: metav1.ObjectMeta{
				Name:      diskName,
				Namespace: vmSet.Namespace,
			},
		}

		err := r.Delete(ctx, disk)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete VirtualDisk %s: %w", diskName, err)
		}
	}
	return nil
}

func (r *VMSetReconciler) constructVM(vmSet *v1beta1.VMSet, ordinal int32) *v1beta1.VirtualMachine {
	vmName := fmt.Sprintf("%s-%d", vmSet.Name, ordinal)

	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: vmSet.Namespace,
			Labels:    r.getLabelsForOrdinal(vmSet, ordinal),
		},
		Spec: vmSet.Spec.Template.Spec,
	}

	// Inject VirtualDisk references BEFORE VM creation
	r.injectVirtualDiskReferences(vm, vmSet, ordinal)

	return vm
}

func (r *VMSetReconciler) injectVirtualDiskReferences(vm *v1beta1.VirtualMachine, vmSet *v1beta1.VMSet, ordinal int32) {
	// This method modifies the VM spec to reference the created VirtualDisks
	for _, diskTemplate := range vmSet.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-%d", vmSet.Name, diskTemplate.MetaData.Name, ordinal)

		for _, vol := range vm.Spec.Volumes {
			if vol.VirtualDisk != nil && vol.VirtualDisk.VirtualDiskName == diskTemplate.MetaData.Name {
				vol.VirtualDisk.VirtualDiskName = diskName
			}
		}
	}
}

func (r *VMSetReconciler) getCurrentVMs(ctx context.Context, vmSet *v1beta1.VMSet) ([]v1beta1.VirtualMachine, error) {
	vmList := &v1beta1.VirtualMachineList{}

	selector, err := metav1.LabelSelectorAsSelector(&vmSet.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	err = r.List(ctx, vmList,
		client.InNamespace(vmSet.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)

	if err != nil {
		return nil, err
	}

	return vmList.Items, nil
}

func (r *VMSetReconciler) getLabelsForOrdinal(vmSet *v1beta1.VMSet, ordinal int32) map[string]string {
	labels := make(map[string]string)

	// Copy template labels
	for k, v := range vmSet.Spec.Template.Metadata.Labels {
		labels[k] = v
	}

	// Add VMSet-specific labels
	labels["cloudhypervisor.quill.today/vmset-name"] = vmSet.Name
	labels["cloudhypervisor.quill.today/vmset-ordinal"] = strconv.Itoa(int(ordinal))
	labels[VMSET_LABEL] = vmSet.Labels[VMSET_LABEL]

	return labels
}

func (r *VMSetReconciler) updateStatus(ctx context.Context, vmSet *v1beta1.VMSet, currentVMs []v1beta1.VirtualMachine) (ctrl.Result, error) {
	status := v1beta1.VMSetStatus{
		Replicas: int32(len(currentVMs)),
	}

	// Count ready VMs
	readyCount := int32(0)
	availableCount := int32(0)

	for _, vm := range currentVMs {
		// Check VM status - implementation depends on your VM status structure
		if isVMReady(&vm) {
			readyCount++
		}
		if isVMAvailable(&vm) {
			availableCount++
		}
	}

	status.ReadyReplicas = readyCount
	status.AvailableReplicas = availableCount

	vmSet.Status = status
	return ctrl.Result{}, r.Status().Update(ctx, vmSet)
}

func (r *VMSetReconciler) handleDeletion(ctx context.Context, vmSet *v1beta1.VMSet) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get all VMs managed by this VMSet
	currentVMs, err := r.getCurrentVMs(ctx, vmSet)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete all VMs and their VirtualDisks
	for _, vm := range currentVMs {
		ordinal := getOrdinal(vm.Name)

		// Delete VM
		err := r.Delete(ctx, &vm)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete VM during cleanup", "name", vm.Name)
			return ctrl.Result{}, err
		}

		// Delete associated VirtualDisks
		if r.shouldDeleteVirtualDisksOnDelete(vmSet) {
			err = r.deleteVirtualDisks(ctx, vmSet, ordinal)
			if err != nil {
				log.Error(err, "Failed to delete VirtualDisks during cleanup", "ordinal", ordinal)
				return ctrl.Result{}, err
			}

		}

	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(vmSet, VMSetProtectionFinalizer)
	return ctrl.Result{}, r.Update(ctx, vmSet)
}

// Helper functions
func getOrdinal(vmName string) int32 {
	parts := strings.Split(vmName, "-")
	if len(parts) == 0 {
		return 0
	}

	ordinal, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0
	}

	return int32(ordinal)
}

func isVMReady(vm *v1beta1.VirtualMachine) bool {
	// Implementation depends on your VM status structure
	// Return true if VM is ready
	return vm.Status.Phase == "Running"
}

func isVMAvailable(vm *v1beta1.VirtualMachine) bool {
	// Implementation depends on your VM status structure
	// Return true if VM is available
	return vm.Status.Phase == "Running"
}

// SetupWithManager sets up the controller with the Manager.
func (r *VMSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VMSet{}).
		Owns(&v1beta1.VirtualMachine{}).
		Owns(&v1beta1.VirtualDisk{}).
		Named("vmset").
		Complete(r)
}
