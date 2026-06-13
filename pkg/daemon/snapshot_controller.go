package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nalajala4naresh/ch-vmm/pkg/cloudhypervisor"
	"github.com/nalajala4naresh/ch-vmm/pkg/cloudutils"
	"github.com/nalajala4naresh/ch-vmm/pkg/volumeutil"
	"github.com/nalajala4naresh/chvmm-api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

type VMSnapShotReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	NodeName string
	NodeIP   string
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots/finalizers,verbs=update
//

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VMSnapShot object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *VMSnapShotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var snap v1beta1.VMSnapShot

	if err := r.Get(ctx, req.NamespacedName, &snap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := snap.Status.DeepCopy()

	vmKey := types.NamespacedName{Namespace: req.Namespace, Name: snap.Spec.VmName}

	var vm v1beta1.VirtualMachine
	if err := r.Get(ctx, vmKey, &vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	shouldReconcile := (vm.Status.NodeName != "" && vm.Status.NodeName == r.NodeName) ||
		(vm.Status.Migration != nil && vm.Status.Migration.TargetNodeName != "" && vm.Status.Migration.TargetNodeName == r.NodeName)

	// If the node is not responsible, say OK
	if !shouldReconcile {
		return ctrl.Result{}, nil
	}

	var rerror error
	requeueAfter := time.Second * 5 // Default backoff period

	switch snap.Status.VMSnapShotStatusPhase {
	case v1beta1.VMSnapShotScheduled:
		if vm.Status.Phase != v1beta1.VirtualMachineRunning {
			ctrl.LoggerFrom(ctx).Info("VM is not running, requeuing snapshot creation")
			rerror = fmt.Errorf("VM not in running state")
			break
		}

		// Update VM status to indicate snapshot in progress
		vm.Status.Phase = v1beta1.VirtualMachineSnapShotInprogress
		if err := r.Status().Update(ctx, &vm); err != nil {
			if apierrors.IsConflict(err) {
				ctrl.LoggerFrom(ctx).Info("Conflict updating VM status, requeuing...")
				return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter}, nil
			}
			rerror = fmt.Errorf("failed to update VM status: %w", err)
			break
		}

		snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapShotInProgress

	case v1beta1.VMSnapShotInProgress:
		defer r.updateVMStatus(ctx, &vm)
		ctrl.LoggerFrom(ctx).Info("Snapshot is in progress")
		rerror = r.createVMSnapShot(ctx, &vm, &snap)
		if rerror != nil {
			ctrl.LoggerFrom(ctx).Error(rerror, "Snapshot creation failed")
			snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapshotFailed
			break
		}

		snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapshotCreated

	case v1beta1.VMSnapshotCreated:
		ctrl.LoggerFrom(ctx).Info("Snapshot completed successfully")
		rerror = r.createRestoreSpec(ctx, &snap, &vm)
		if rerror != nil {
			ctrl.LoggerFrom(ctx).Error(rerror, "VMRestoreSpec creation failed")
			break
		}
		snap.Status.VMSnapShotStatusPhase = v1beta1.VMRestoreSpecCreated

	case v1beta1.VMRestoreSpecCreated:
		// Wait for all VirtualDiskSnapshots to be ReadyToUse before marking VMSnapShot as ready
		ctrl.LoggerFrom(ctx).Info("Checking if all VirtualDiskSnapshots owned by VMSnapShot are ready")

		ctrl.LoggerFrom(ctx).Info("Checking disk status", "totalDisks", len(snap.Status.DiskStatus))

		allDiskSnapshotsReady := true
		var pendingSnapshots []string
		var failedSnapshots []string

		// Update DiskStatus with current VirtualDiskSnapshot status and check if all are ready
		var updatedDiskStatus []v1beta1.DiskStatus
		for _, diskStatus := range snap.Status.DiskStatus {
			ctrl.LoggerFrom(ctx).Info("Checking disk status", "name", diskStatus.Name, "ready", diskStatus.Ready, "message", diskStatus.Message)

			// Get current VirtualDiskSnapshot status
			vds := &v1beta1.VirtualDiskSnapshot{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: vm.Namespace,
				Name:      diskStatus.Name,
			}, vds)

			if err != nil {
				if apierrors.IsNotFound(err) {
					// VirtualDiskSnapshot was deleted manually
					ctrl.LoggerFrom(ctx).Error(fmt.Errorf("VirtualDiskSnapshot not found"), "VirtualDiskSnapshot was deleted", "name", diskStatus.Name)
					failedSnapshots = append(failedSnapshots, diskStatus.Name)
					allDiskSnapshotsReady = false

					// Update disk status to reflect deletion
					updatedDiskStatus = append(updatedDiskStatus, v1beta1.DiskStatus{
						Name:    diskStatus.Name,
						Ready:   false,
						Message: "VirtualDiskSnapshot was deleted",
						Reason:  "VirtualDiskSnapshotNotFound",
					})
					continue
				}
				ctrl.LoggerFrom(ctx).Error(err, "Failed to get VirtualDiskSnapshot", "name", diskStatus.Name)
				return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter}, err
			}

			// Update disk status based on current VirtualDiskSnapshot status
			var newDiskStatus v1beta1.DiskStatus
			newDiskStatus.Name = diskStatus.Name

			if vds.Status.Phase == v1beta1.VirtualDiskSnapshotReadyToUse {
				ctrl.LoggerFrom(ctx).Info("VirtualDiskSnapshot is ready", "name", diskStatus.Name)
				newDiskStatus.Ready = true
				newDiskStatus.Message = "VirtualDiskSnapshot is ready"
				newDiskStatus.Reason = "VirtualDiskSnapshotReady"
			} else {
				allDiskSnapshotsReady = false
				// Check if it's a failure or just pending
				if vds.Status.Phase == v1beta1.VirtualDiskSnapshotError {
					failedSnapshots = append(failedSnapshots, diskStatus.Name)
					ctrl.LoggerFrom(ctx).Info("VirtualDiskSnapshot in Error state", "name", diskStatus.Name, "errorMessage", vds.Status.ErrorMessage)
					newDiskStatus.Ready = false
					newDiskStatus.Message = "VirtualDiskSnapshot failed"
					newDiskStatus.Reason = "VirtualDiskSnapshotError"
				} else {
					pendingSnapshots = append(pendingSnapshots, diskStatus.Name)
					ctrl.LoggerFrom(ctx).Info("VirtualDiskSnapshot still pending", "name", diskStatus.Name, "phase", vds.Status.Phase)
					newDiskStatus.Ready = false
					newDiskStatus.Message = fmt.Sprintf("VirtualDiskSnapshot is in %s phase", vds.Status.Phase)
					newDiskStatus.Reason = "VirtualDiskSnapshotPending"
				}
			}

			updatedDiskStatus = append(updatedDiskStatus, newDiskStatus)
		}

		// Update the VMSnapShot's DiskStatus with current information
		snap.Status.DiskStatus = updatedDiskStatus

		// Check if any disk failed - if so, fail the entire snapshot
		if !allDiskSnapshotsReady {
			if len(failedSnapshots) > 0 {
				// Some snapshots Pending
				ctrl.LoggerFrom(ctx).Error(fmt.Errorf("one or more VirtualDiskSnapshots failed"), "VirtualDiskSnapshot failed, marking VMSnapShot as failed", "failedSnapshots", failedSnapshots)
				snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapshotFailed

			} else if len(pendingSnapshots) > 0 {
				ctrl.LoggerFrom(ctx).Info("Waiting for VirtualDiskSnapshots to be ready", "pendingSnapshots", pendingSnapshots)
				// Some snapshots Pending
				return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter}, nil

			}
		}

		// All disk snapshots are ready
		ctrl.LoggerFrom(ctx).Info("All disk snapshots are ready, marking VMSnapShot as ready", "totalDisks", len(snap.Status.DiskStatus))
		r.Recorder.Eventf(&snap, corev1.EventTypeNormal, "AllDiskSnapshotsReady", "All disk snapshots are ready (%d disks)", len(snap.Status.DiskStatus))

		// Mark VMSnapShot as ready (you might want to add a new phase like VMSnapShotReady)
		// For now, we'll keep it in VMRestoreSpecCreated but with all disks ready
		snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapShotReady

	case v1beta1.VMSnapshotFailed:
		ctrl.LoggerFrom(ctx).Info("Snapshot is in failed state, not requeuing automatically")
		return ctrl.Result{}, nil

	default:
		ctrl.LoggerFrom(ctx).Info("Unhandled snapshot phase, requeuing...", "phase", snap.Status.VMSnapShotStatusPhase)

	}

	// Always update the snapshot status if it has changed
	if !reflect.DeepEqual(snap.Status, status) {
		ctrl.LoggerFrom(ctx).Info("Updating snapshot status", "oldStatus", status, "newStatus", snap.Status)
		if err := r.Status().Update(ctx, &snap); err != nil {
			if apierrors.IsConflict(err) {
				ctrl.LoggerFrom(ctx).Info("Conflict updating snapshot status, requeuing...")
				return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter}, nil
			}
			return ctrl.Result{}, fmt.Errorf("failed to update snapshot status: %w", err)
		}
	}

	if rerror != nil {
		return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func (r *VMSnapShotReconciler) createRestoreSpec(ctx context.Context, snap *v1beta1.VMSnapShot, vm *v1beta1.VirtualMachine) error {
	var namespace string
	if snap.Namespace == "" {
		namespace = "default"
	} else {
		namespace = snap.Namespace
	}
	restorespec := v1beta1.VMRestoreSpec{ObjectMeta: metav1.ObjectMeta{
		Name:      snap.Name,
		Namespace: namespace,
		Labels:    map[string]string{volumeutil.VM_LABEL: vm.Name, volumeutil.SNAP_LABEL: snap.Name},
	},
		Spec: v1beta1.VMRestoreSpecSpec{VMName: vm.Name, VMSpec: vm.Spec}}
	err := r.Client.Create(ctx, &restorespec)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (r *VMSnapShotReconciler) createVMSnapShot(ctx context.Context, vm *v1beta1.VirtualMachine, snap *v1beta1.VMSnapShot) error {

	snapdestinationFromNode := filepath.Join(getVMDataDirPath(vm), snap.Name)

	if _, err := os.Stat(snapdestinationFromNode); os.IsNotExist(err) {
		fmt.Printf("creating destination %s for snapshot %s", snapdestinationFromNode, snap.Name)
		err := os.MkdirAll(snapdestinationFromNode, 0777)
		if err != nil {
			return err

		}

		defer os.RemoveAll(snapdestinationFromNode)

	}

	hyperPath := "/var/run/ch-vmm/"
	hyperDestination := filepath.Join(hyperPath, snap.Name)
	// take the  snapshot
	var rerr error
	r.Recorder.Eventf(snap, corev1.EventTypeNormal, "SnapShotInProgress", "snapshot is in progress")

	if !snap.Spec.SkipMemorySnapshot {
		rerr = r.chSnapshot(ctx, vm, hyperDestination)

		memStatus := &v1beta1.MemoryStatus{Bucket: snap.Spec.Bucket}
		if rerr != nil {
			//record the event
			memStatus.Ready = false
			memStatus.Message = rerr.Error()
			memStatus.Reason = "HypervisorSnapshotFailed"
			snap.Status.MemoryStatus = *memStatus
			r.Recorder.Eventf(snap, corev1.EventTypeWarning, "FailedToCreateMemorySnapshot", "failed to create snapshot due to %s", rerr)
			return rerr

		}

		key, rerr := r.uploadToBucket(snapdestinationFromNode, snap.Name, snap.Spec.Bucket)
		memStatus.ObjectKey = key
		if rerr != nil {
			//record the event
			memStatus.Message = rerr.Error()
			memStatus.Reason = "FailedToUploadToBucket"
			memStatus.Ready = false
			snap.Status.MemoryStatus = *memStatus
			r.Recorder.Eventf(snap, corev1.EventTypeWarning, "FailedToUploadMemorySnapshot", "failed to upload snapshot to bucket %s", snap.Spec.Bucket)
			return rerr

		}
		memStatus.Ready = true
		memStatus.Reason = "MemorySnapshotCreated"
		snap.Status.MemoryStatus = *memStatus

	}

	//now take the snapshot of disk images
	rerr = r.snapshotDisks(ctx, snap, vm)

	return rerr
}

func (r *VMSnapShotReconciler) updateVMStatus(ctx context.Context, vm *v1beta1.VirtualMachine) {

	vm.Status.Phase = v1beta1.VirtualMachineRunning
	r.Status().Update(ctx, vm)
}

func (r *VMSnapShotReconciler) snapshotDisks(ctx context.Context, snap *v1beta1.VMSnapShot, vm *v1beta1.VirtualMachine) error {
	for _, vol := range vm.Spec.Volumes {
		ctrl.LoggerFrom(ctx).Info("Creating VirtualDiskSnapshot for volume", "volume", vol.Name)

		// Only create snapshots for volumes that represent persistent storage
		if vol.VirtualDisk != nil {
			// Create VirtualDiskSnapshot instead of direct PVC snapshot
			virtualDiskSnapshotName := fmt.Sprintf("%s-%s-vds", vol.Name, snap.Name)

			// Check if VirtualDiskSnapshot already exists
			existingVDS := &v1beta1.VirtualDiskSnapshot{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: vm.Namespace,
				Name:      virtualDiskSnapshotName,
			}, existingVDS)

			if err != nil && !apierrors.IsNotFound(err) {
				ctrl.LoggerFrom(ctx).Error(err, "Failed to check existing VirtualDiskSnapshot", "name", virtualDiskSnapshotName)
				return err
			}

			if apierrors.IsNotFound(err) {
				// Create new VirtualDiskSnapshot
				vds := &v1beta1.VirtualDiskSnapshot{
					ObjectMeta: metav1.ObjectMeta{
						Name:      virtualDiskSnapshotName,
						Namespace: vm.Namespace,
						Labels: map[string]string{
							"vm":       vm.Name,
							"snapshot": snap.Name,
							"volume":   vol.Name,
						},
					},
					Spec: v1beta1.VirtualDiskSnapshotSpec{
						VirtualDiskName: vol.VirtualDisk.VirtualDiskName,
						SkipSnapshot:    false,
					},
				}

				// Set VMSnapShot as owner of VirtualDiskSnapshot
				if err := controllerutil.SetControllerReference(snap, vds, r.Scheme); err != nil {
					ctrl.LoggerFrom(ctx).Error(err, "Failed to set controller reference for VirtualDiskSnapshot")
					return err
				}

				err = r.Create(ctx, vds)
				if err != nil {
					ctrl.LoggerFrom(ctx).Error(err, "Failed to create VirtualDiskSnapshot", "name", virtualDiskSnapshotName)

					diskStatus := v1beta1.DiskStatus{
						Name:    virtualDiskSnapshotName,
						Ready:   false,
						Message: "VirtualDiskSnapshotCreationFailed",
						Reason:  err.Error(),
					}
					snap.Status.DiskStatus = append(snap.Status.DiskStatus, diskStatus)
					r.Recorder.Eventf(snap, corev1.EventTypeWarning, "VirtualDiskSnapshotCreationFailed", "Failed to create VirtualDiskSnapshot for volume %s: %s", vol.Name, err)
					return err
				}

				diskStatus := v1beta1.DiskStatus{
					Name:    virtualDiskSnapshotName,
					Ready:   false,
					Message: "VirtualDiskSnapshotCreated",
					Reason:  "VirtualDiskSnapshotCreated",
				}

				snap.Status.DiskStatus = append(snap.Status.DiskStatus, diskStatus)

				ctrl.LoggerFrom(ctx).Info("Created VirtualDiskSnapshot", "name", virtualDiskSnapshotName)
				r.Recorder.Eventf(snap, corev1.EventTypeNormal, "VirtualDiskSnapshotCreated", "Created VirtualDiskSnapshot for volume %s", vol.Name)
				continue
			}

		}
	}

	return nil
}

func (r *VMSnapShotReconciler) chSnapshot(ctx context.Context, vm *v1beta1.VirtualMachine, dest string) error {

	chClient := getCloudHypervisorClient(vm)

	err := chClient.VmPause(ctx)
	if err != nil {
		return err
	}
	// always resume the VM
	defer chClient.VmResume(ctx)

	//create snapshot
	destionationUrl := "file://" + dest
	snapshot := cloudhypervisor.VmSnapshotConfig{DestinationUrl: destionationUrl}
	serr := chClient.VmSnapshot(ctx, &snapshot)
	if serr != nil {
		ctrl.Log.Error(serr, "Unable to take firecracker  snapshot", "vm", vm.Name)
	}
	return serr
}

// todo: upload support for gcs and s3 versions
func (r *VMSnapShotReconciler) uploadToBucket(destination string, snapshotName string, bucket string) (string, error) {
	dirEntries, err := os.ReadDir(destination)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %v", err)
	}
	files := []string{}
	for _, file := range dirEntries {
		files = append(files, filepath.Join(destination, file.Name()))
	}

	archd := filepath.Join(snapshotName + ".zstd.tar")
	key := snapshotName + ".zstd.tar"
	defer os.RemoveAll(archd)
	err = volumeutil.CreateArchive(archd, files)
	if err != nil {
		ctrl.Log.Error(err, "failed to create archive", "snapshot", snapshotName)
		return "", err
	}
	if strings.HasPrefix(bucket, "s3") {
		bucket = strings.TrimPrefix(bucket, "s3://")
		err = cloudutils.UploadObjectToS3(bucket, archd, archd)

	} else {
		bucket = strings.TrimPrefix(bucket, "gcs://")
		err = cloudutils.UploadObjectToGCS(bucket, archd, key)
	}

	return key, err

}

func (r *VMSnapShotReconciler) findVMSnapShotsForVirtualDiskSnapshot(ctx context.Context, obj client.Object) []ctrl.Request {
	vds := obj.(*v1beta1.VirtualDiskSnapshot)

	// Find VMSnapShot that owns this VirtualDiskSnapshot
	vmsnapshots := &v1beta1.VMSnapShotList{}
	err := r.List(ctx, vmsnapshots, client.InNamespace(vds.Namespace))
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list VMSnapShots when finding owner for VirtualDiskSnapshot", "virtualDiskSnapshot", vds.Name)
		return []ctrl.Request{}
	}

	var requests []ctrl.Request
	for _, vmsnap := range vmsnapshots.Items {
		// Check if this VMSnapShot owns the VirtualDiskSnapshot
		if metav1.IsControlledBy(vds, &vmsnap) {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmsnap.Name,
					Namespace: vmsnap.Namespace,
				},
			})
			ctrl.LoggerFrom(ctx).Info("Found VMSnapShot owner for VirtualDiskSnapshot",
				"virtualDiskSnapshot", vds.Name,
				"vmsnap", vmsnap.Name,
				"phase", vds.Status.Phase)
			break
		}
	}

	return requests
}

func (r *VMSnapShotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VMSnapShot{}).
		Watches(
			&v1beta1.VirtualDiskSnapshot{},
			handler.EnqueueRequestsFromMapFunc(r.findVMSnapShotsForVirtualDiskSnapshot),
		).
		Named("vmsnapshot-daemon").
		Complete(r)
}
