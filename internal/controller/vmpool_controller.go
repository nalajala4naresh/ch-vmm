package controller

import (
	"context"
	"crypto/rand"
	"fmt"
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
	VMPoolProtectionFinalizer = "cloudhypervisor.quill.today/vmpool-protection"
)

const VMPOOL_LABEL = "microbox-api/vmpool-id"

// VMPoolReconciler reconciles a VMPool object
type VMPoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmpools/finalizers,verbs=update

// Reconcile handles VMPool resources
func (r *VMPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pool v1beta1.VMPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if pool.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &pool)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&pool, VMPoolProtectionFinalizer) {
		controllerutil.AddFinalizer(&pool, VMPoolProtectionFinalizer)
		return ctrl.Result{}, r.Update(ctx, &pool)
	}

	// Reconcile the VMPool
	return r.reconcileVMPool(ctx, &pool)
}

func (r *VMPoolReconciler) reconcileVMPool(ctx context.Context, pool *v1beta1.VMPool) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get current VMs managed by this VMPool
	currentVMs, err := r.getCurrentVMs(ctx, pool)
	if err != nil {
		log.Error(err, "Failed to get current VMs")
		return ctrl.Result{}, err
	}

	// Calculate desired replicas
	desiredReplicas := int32(0)
	if pool.Spec.Replicas != nil {
		desiredReplicas = *pool.Spec.Replicas
	}

	currentReplicas := int32(len(currentVMs))

	// Scale up or down as needed
	if currentReplicas < desiredReplicas {
		// Scale up
		return r.scaleUp(ctx, pool, currentVMs, desiredReplicas)
	} else if currentReplicas > desiredReplicas {
		// Scale down
		return r.scaleDown(ctx, pool, currentVMs, desiredReplicas)
	}

	// Update status
	return r.updateStatus(ctx, pool, currentVMs)
}

func (r *VMPoolReconciler) scaleUp(ctx context.Context, pool *v1beta1.VMPool, currentVMs []v1beta1.VirtualMachine, desiredReplicas int32) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Create one VM at a time for controlled scaling
	toCreate := desiredReplicas - int32(len(currentVMs))
	if toCreate > 0 {
		// Generate unique random name for new VM
		vmName := r.generateRandomVMName(pool)

		// Check if VM already exists to prevent duplicate creation
		existingVM := &v1beta1.VirtualMachine{}
		err := r.Get(ctx, types.NamespacedName{Name: vmName, Namespace: pool.Namespace}, existingVM)
		if err == nil {
			// VM already exists, skip creation
			log.Info("VM already exists, skipping creation", "vmName", vmName)
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		} else if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to check VM existence", "vmName", vmName)
			return ctrl.Result{}, err
		}

		// Create VirtualDisks first
		virtualDiskNames, err := r.createVirtualDisksForVM(ctx, pool, vmName)
		if err != nil {
			log.Error(err, "Failed to create VirtualDisks for VM", "vmName", vmName)
			return ctrl.Result{}, err
		}

		// Create the VM with VirtualDisk references already injected
		vm := r.constructVM(pool, vmName, virtualDiskNames)
		if err := controllerutil.SetControllerReference(pool, vm, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		err = r.Create(ctx, vm)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create VM", "vmName", vmName)
			return ctrl.Result{}, err
		}

		log.Info("Successfully created VM with VirtualDisks", "name", vm.Name)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	return ctrl.Result{}, nil
}

func (r *VMPoolReconciler) scaleDown(ctx context.Context, pool *v1beta1.VMPool, currentVMs []v1beta1.VirtualMachine, desiredReplicas int32) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Delete excess VMs (Deployment-like: no specific order)
	toDelete := int32(len(currentVMs)) - desiredReplicas

	for i := int32(0); i < toDelete; i++ {
		vmToDelete := currentVMs[len(currentVMs)-1-int(i)]

		// Delete VirtualDisks for this VM first (because they have finalizers)
		err := r.deleteVirtualDisksForVM(ctx, pool, vmToDelete.Name)
		if err != nil {
			log.Error(err, "Failed to delete VirtualDisks for VM", "name", vmToDelete.Name)
			return ctrl.Result{}, err
		}

		// Delete VM
		err = r.Delete(ctx, &vmToDelete)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete VM", "name", vmToDelete.Name)
			return ctrl.Result{}, err
		}

		log.Info("Successfully deleted VM and its VirtualDisks", "name", vmToDelete.Name)
	}

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func (r *VMPoolReconciler) createVirtualDisksForVM(ctx context.Context, pool *v1beta1.VMPool, vmName string) ([]string, error) {
	var diskNames []string

	for _, diskTemplate := range pool.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-disk", vmName, diskTemplate.MetaData.Name)

		// Check if VirtualDisk already exists
		existingDisk := &v1beta1.VirtualDisk{}
		err := r.Get(ctx, types.NamespacedName{Name: diskName, Namespace: pool.Namespace}, existingDisk)
		if err == nil {
			// VirtualDisk already exists, skip creation
			log.FromContext(ctx).Info("VirtualDisk already exists, skipping creation", "diskName", diskName)
			diskNames = append(diskNames, diskName)
			continue
		} else if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to check VirtualDisk existence %s: %w", diskName, err)
		}

		disk := &v1beta1.VirtualDisk{
			ObjectMeta: metav1.ObjectMeta{
				Name:        diskName,
				Namespace:   pool.Namespace,
				Annotations: diskTemplate.MetaData.Annotations,
				Labels:      r.getLabelsForVM(pool, vmName),
			},
			Spec: diskTemplate.Spec,
		}

		// Set VMPool as the controller of the VirtualDisk
		if err := controllerutil.SetControllerReference(pool, disk, r.Scheme); err != nil {
			return nil, fmt.Errorf("failed to set VMPool as controller of VirtualDisk %s: %w", diskName, err)
		}

		err = r.Create(ctx, disk)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create VirtualDisk %s: %w", disk.Name, err)
		}

		diskNames = append(diskNames, diskName)
	}

	return diskNames, nil
}

func (r *VMPoolReconciler) constructVM(pool *v1beta1.VMPool, vmName string, virtualDiskNames []string) *v1beta1.VirtualMachine {
	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: pool.Namespace,
			Labels:    r.getLabelsForVM(pool, vmName),
		},
		Spec: pool.Spec.Template.Spec,
	}

	// Inject VirtualDisk references BEFORE VM creation
	if virtualDiskNames != nil {
		r.injectVirtualDiskReferences(vm, pool)
	}

	return vm
}

func (r *VMPoolReconciler) injectVirtualDiskReferences(vm *v1beta1.VirtualMachine, pool *v1beta1.VMPool) {
	// This method modifies the VM spec to reference the created VirtualDisks
	for _, diskTemplate := range pool.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-disk", vm.Name, diskTemplate.MetaData.Name)

		for _, vol := range vm.Spec.Volumes {
			if vol.VirtualDisk != nil && vol.VirtualDisk.VirtualDiskName == diskTemplate.MetaData.Name {
				vol.VirtualDisk.VirtualDiskName = diskName
			}
		}
	}
}

func (r *VMPoolReconciler) generateRandomVMName(pool *v1beta1.VMPool) string {
	// Generate a random suffix for unique naming (Deployment-like)
	randomBytes := make([]byte, 6)
	rand.Read(randomBytes)
	randomSuffix := fmt.Sprintf("%x", randomBytes)[:8]

	baseName := pool.Name

	vmName := fmt.Sprintf("%s-%s", baseName, randomSuffix)
	// Try to find an available name
	return vmName
}

func (r *VMPoolReconciler) getCurrentVMs(ctx context.Context, pool *v1beta1.VMPool) ([]v1beta1.VirtualMachine, error) {
	vmList := &v1beta1.VirtualMachineList{}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	err = r.List(ctx, vmList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)

	if err != nil {
		return nil, err
	}

	return vmList.Items, nil
}

func (r *VMPoolReconciler) getLabelsForVM(pool *v1beta1.VMPool, vmName string) map[string]string {
	labels := make(map[string]string)

	// Copy template labels
	for k, v := range pool.Spec.Template.Metadata.Labels {
		labels[k] = v
	}

	// Add VMPool-specific labels
	labels["vmpool.chvmm.io/name"] = pool.Name
	labels["vmpool.chvmm.io/vm-name"] = vmName
	labels[VMPOOL_LABEL] = pool.Labels[VMPOOL_LABEL]

	return labels
}

func (r *VMPoolReconciler) updateStatus(ctx context.Context, pool *v1beta1.VMPool, currentVMs []v1beta1.VirtualMachine) (ctrl.Result, error) {
	status := v1beta1.VMPoolStatus{
		Replicas: int32(len(currentVMs)),
	}

	// Count ready and available VMs
	readyCount := int32(0)
	availableCount := int32(0)

	for _, vm := range currentVMs {
		if isVMPoolVMReady(&vm) {
			readyCount++
		}
		if isVMPoolVMAvailable(&vm) {
			availableCount++
		}
	}

	status.ReadyReplicas = readyCount
	status.AvailableReplicas = availableCount

	pool.Status = status
	return ctrl.Result{}, r.Status().Update(ctx, pool)
}

func (r *VMPoolReconciler) handleDeletion(ctx context.Context, pool *v1beta1.VMPool) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get all VMs managed by this VMPool
	currentVMs, err := r.getCurrentVMs(ctx, pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete all VMs first
	for _, vm := range currentVMs {
		err := r.Delete(ctx, &vm)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete VM during cleanup", "name", vm.Name)
			return ctrl.Result{}, err
		}
	}

	// Explicitly delete all VirtualDisks owned by this VMPool (because they have finalizers)
	err = r.deleteAllVirtualDisks(ctx, pool)
	if err != nil {
		log.Error(err, "Failed to delete VirtualDisks during cleanup")
		return ctrl.Result{}, err
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(pool, VMPoolProtectionFinalizer)
	return ctrl.Result{}, r.Update(ctx, pool)
}

func (r *VMPoolReconciler) deleteAllVirtualDisks(ctx context.Context, pool *v1beta1.VMPool) error {
	// List all VirtualDisks in the namespace
	virtualDiskList := &v1beta1.VirtualDiskList{}
	err := r.List(ctx, virtualDiskList, client.InNamespace(pool.Namespace))
	if err != nil {
		return fmt.Errorf("failed to list VirtualDisks: %w", err)
	}

	// Delete VirtualDisks that are owned by this VMPool
	for _, disk := range virtualDiskList.Items {
		ownerRef := metav1.GetControllerOf(&disk)
		if ownerRef != nil &&
			ownerRef.APIVersion == v1beta1.GroupVersion.String() &&
			ownerRef.Kind == "VMPool" &&
			ownerRef.Name == pool.Name {

			err := r.Delete(ctx, &disk)
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete VirtualDisk %s: %w", disk.Name, err)
			}
		}
	}

	return nil
}

func (r *VMPoolReconciler) deleteVirtualDisksForVM(ctx context.Context, pool *v1beta1.VMPool, vmName string) error {
	// Delete VirtualDisks that belong to this specific VM
	for _, diskTemplate := range pool.Spec.DiskTemplates {
		diskName := fmt.Sprintf("%s-%s-disk", vmName, diskTemplate.MetaData.Name)

		disk := &v1beta1.VirtualDisk{
			ObjectMeta: metav1.ObjectMeta{
				Name:      diskName,
				Namespace: pool.Namespace,
			},
		}

		err := r.Delete(ctx, disk)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete VirtualDisk %s: %w", diskName, err)
		}
	}

	return nil
}

// Helper functions for VMPool
func isVMPoolVMReady(vm *v1beta1.VirtualMachine) bool {
	// Implementation depends on your VM status structure
	// Return true if VM is ready
	return vm.Status.Phase == "Running"
}

func isVMPoolVMAvailable(vm *v1beta1.VirtualMachine) bool {
	// Implementation depends on your VM status structure
	// Return true if VM is available
	return vm.Status.Phase == "Running"
}

// SetupWithManager sets up the controller with the Manager.
func (r *VMPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VMPool{}).
		Owns(&v1beta1.VirtualMachine{}).
		Owns(&v1beta1.VirtualDisk{}).
		Named("vmpool").
		Complete(r)
}
