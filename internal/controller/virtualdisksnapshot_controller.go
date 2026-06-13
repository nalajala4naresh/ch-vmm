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
//kubebuilder create api --group cloudhypervisor.quill.today --version v1beta1 --kind VirtualDiskSnapshot --resource=false --controller=true
package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const (
	VirtualDiskSnapshotProtectionFinalizer = "cloudhypervisor.quill.today/virtualdisksnapshot-protection"
)

// VirtualDiskSnapshotReconciler reconciles a VirtualDiskSnapshot object
type VirtualDiskSnapshotReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisksnapshots/finalizers,verbs=update
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *VirtualDiskSnapshotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the VirtualDiskSnapshot instance
	virtualDiskSnapshot := &v1beta1.VirtualDiskSnapshot{}
	err := r.Get(ctx, req.NamespacedName, virtualDiskSnapshot)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("VirtualDiskSnapshot resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get VirtualDiskSnapshot")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if virtualDiskSnapshot.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, virtualDiskSnapshot)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(virtualDiskSnapshot, VirtualDiskSnapshotProtectionFinalizer) {
		controllerutil.AddFinalizer(virtualDiskSnapshot, VirtualDiskSnapshotProtectionFinalizer)
		return ctrl.Result{}, r.Update(ctx, virtualDiskSnapshot)
	}

	// Reconcile the VirtualDiskSnapshot
	return r.reconcileVirtualDiskSnapshot(ctx, virtualDiskSnapshot)
}

func (r *VirtualDiskSnapshotReconciler) reconcileVirtualDiskSnapshot(ctx context.Context, virtualDiskSnapshot *v1beta1.VirtualDiskSnapshot) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if VolumeSnapshot already exists
	volumeSnapshotName := virtualDiskSnapshot.Name
	existingVolumeSnapshot := &snapv1.VolumeSnapshot{}
	err := r.Get(ctx, client.ObjectKey{Namespace: virtualDiskSnapshot.Namespace, Name: volumeSnapshotName}, existingVolumeSnapshot)
	if err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to get existing VolumeSnapshot")
		return ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		// Create VolumeSnapshot
		err = r.createVolumeSnapshot(ctx, virtualDiskSnapshot)
		if err != nil {
			log.Error(err, "Failed to create VolumeSnapshot")
			r.Recorder.Event(virtualDiskSnapshot, "Warning", "VolumeSnapshotCreationFailed", fmt.Sprintf("Failed to create VolumeSnapshot: %v", err))
			return ctrl.Result{}, err
		}
		log.Info("Created VolumeSnapshot", "name", volumeSnapshotName)
		r.Recorder.Event(virtualDiskSnapshot, "Normal", "VolumeSnapshotCreated", fmt.Sprintf("Created VolumeSnapshot %s", volumeSnapshotName))
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Map VolumeSnapshot status to VirtualDiskSnapshot status
	statusUpdated := r.mapVolumeSnapshotStatusToVirtualDiskSnapshot(ctx, virtualDiskSnapshot, existingVolumeSnapshot)
	if statusUpdated {
		err = r.Status().Update(ctx, virtualDiskSnapshot)
		if err != nil {
			log.Error(err, "Failed to update VirtualDiskSnapshot status")
			return ctrl.Result{}, err
		}
	}

	// If VolumeSnapshot is not ready, requeue to check again
	if !r.isVolumeSnapshotReady(existingVolumeSnapshot) {
		log.Info("VolumeSnapshot not ready yet", "name", volumeSnapshotName, "status", existingVolumeSnapshot.Status.ReadyToUse)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	return ctrl.Result{}, nil
}

func (r *VirtualDiskSnapshotReconciler) createVolumeSnapshot(ctx context.Context, virtualDiskSnapshot *v1beta1.VirtualDiskSnapshot) error { // Create VolumeSnapshot
	volumeSnapshot := &snapv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      virtualDiskSnapshot.Name,
			Namespace: virtualDiskSnapshot.Namespace,
			Labels:    virtualDiskSnapshot.Labels,
		},
		Spec: snapv1.VolumeSnapshotSpec{
			Source: snapv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &virtualDiskSnapshot.Spec.VirtualDiskName,
			},
		},
	}

	// Set VirtualDiskSnapshot as owner of VolumeSnapshot
	if err := controllerutil.SetControllerReference(virtualDiskSnapshot, volumeSnapshot, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	return r.Create(ctx, volumeSnapshot)
}

func (r *VirtualDiskSnapshotReconciler) isVolumeSnapshotReady(volumeSnapshot *snapv1.VolumeSnapshot) bool {
	return volumeSnapshot.Status.ReadyToUse != nil && *volumeSnapshot.Status.ReadyToUse
}

func (r *VirtualDiskSnapshotReconciler) mapVolumeSnapshotStatusToVirtualDiskSnapshot(ctx context.Context, virtualDiskSnapshot *v1beta1.VirtualDiskSnapshot, volumeSnapshot *snapv1.VolumeSnapshot) bool {
	log := log.FromContext(ctx)
	statusUpdated := false

	// Determine VirtualDiskSnapshot phase based on VolumeSnapshot status
	var newPhase string

	// Check VolumeSnapshot status fields
	if volumeSnapshot.Status.Error != nil {
		// VolumeSnapshot has an error
		newPhase = string(v1beta1.VirtualDiskSnapshotError)
	} else if volumeSnapshot.Status.ReadyToUse != nil && *volumeSnapshot.Status.ReadyToUse {
		// VolumeSnapshot is ready
		newPhase = string(v1beta1.VirtualDiskSnapshotReadyToUse)
	} else if volumeSnapshot.Status.CreationTime != nil {
		// VolumeSnapshot is being created
		newPhase = string(v1beta1.VirtualDiskSnapshotInProgress)
	} else {
		// VolumeSnapshot is pending
		newPhase = string(v1beta1.VirtualDiskSnapshotPending)
	}

	// Update phase if it has changed
	if string(virtualDiskSnapshot.Status.Phase) != newPhase {
		virtualDiskSnapshot.Status.Phase = v1beta1.VirtualDiskSnapshotPhase(newPhase)
		statusUpdated = true
		log.Info("Updated VirtualDiskSnapshot phase", "name", virtualDiskSnapshot.Name, "phase", newPhase)
	}

	// Update conditions based on VolumeSnapshot status
	if volumeSnapshot.Status.ReadyToUse != nil && *volumeSnapshot.Status.ReadyToUse {
		// VolumeSnapshot is ready
		virtualDiskSnapshot.Status.Conditions = []metav1.Condition{
			{
				Type:               string(v1beta1.VirtualDiskSnapshotReady),
				Status:             metav1.ConditionTrue,
				Reason:             "VolumeSnapshotReady",
				Message:            "VolumeSnapshot is ready for use",
				LastTransitionTime: metav1.Now(),
			},
		}
		statusUpdated = true
	} else if volumeSnapshot.Status.Error != nil {
		// VolumeSnapshot has an error
		virtualDiskSnapshot.Status.Conditions = []metav1.Condition{
			{
				Type:               string(v1beta1.VirtualDiskSnapshotError),
				Status:             metav1.ConditionTrue,
				Reason:             "VolumeSnapshotError",
				Message:            fmt.Sprintf("VolumeSnapshot failed: %s", *volumeSnapshot.Status.Error.Message),
				LastTransitionTime: metav1.Now(),
			},
		}
		statusUpdated = true
	} else {
		// VolumeSnapshot is being created or pending
		virtualDiskSnapshot.Status.Conditions = []metav1.Condition{
			{
				Type:               string(v1beta1.VirtualDiskSnapshotCreated),
				Status:             metav1.ConditionFalse,
				Reason:             "VolumeSnapshotCreating",
				Message:            "VolumeSnapshot is being created",
				LastTransitionTime: metav1.Now(),
			},
		}
		statusUpdated = true
	}

	// Emit events for significant status changes
	if statusUpdated {
		switch newPhase {
		case string(v1beta1.VirtualDiskSnapshotReadyToUse):
			r.Recorder.Event(virtualDiskSnapshot, "Normal", "VirtualDiskSnapshotReady", "VirtualDiskSnapshot is ready")
		case string(v1beta1.VirtualDiskSnapshotError):
			r.Recorder.Event(virtualDiskSnapshot, "Warning", "VirtualDiskSnapshotFailed", "VirtualDiskSnapshot failed")
		case "Creating":
			r.Recorder.Event(virtualDiskSnapshot, "Normal", "VirtualDiskSnapshotCreating", "VirtualDiskSnapshot is being created")
		}
	}

	return statusUpdated
}

func (r *VirtualDiskSnapshotReconciler) handleDeletion(ctx context.Context, virtualDiskSnapshot *v1beta1.VirtualDiskSnapshot) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if VolumeSnapshot exists and delete it
	volumeSnapshotName := virtualDiskSnapshot.Name
	volumeSnapshot := &snapv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volumeSnapshotName,
			Namespace: virtualDiskSnapshot.Namespace,
		},
	}

	err := r.Delete(ctx, volumeSnapshot)
	if err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to delete VolumeSnapshot", "name", volumeSnapshotName)
		return ctrl.Result{}, err
	}

	if err == nil {
		log.Info("Deleted VolumeSnapshot", "name", volumeSnapshotName)
		r.Recorder.Event(virtualDiskSnapshot, "Normal", "VolumeSnapshotDeleted", fmt.Sprintf("Deleted VolumeSnapshot %s", volumeSnapshotName))
	}

	// Remove finalizer after VolumeSnapshot is deleted
	if controllerutil.ContainsFinalizer(virtualDiskSnapshot, VirtualDiskSnapshotProtectionFinalizer) {
		controllerutil.RemoveFinalizer(virtualDiskSnapshot, VirtualDiskSnapshotProtectionFinalizer)
		if err := r.Update(ctx, virtualDiskSnapshot); err != nil {
			return ctrl.Result{}, err
		}
	}

	log.Info("VirtualDiskSnapshot deleted", "name", virtualDiskSnapshot.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualDiskSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualDiskSnapshot{}).
		Owns(&snapv1.VolumeSnapshot{}).
		Named("virtualdisksnapshot").
		Complete(r)
}
