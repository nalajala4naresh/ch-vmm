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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	cdiv1beta1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	VirtualDiskProtectionFinalizer = "cloudhypervisor.quill.today/virtualdisk-protection"
)

// VirtualDiskReconciler reconciles a VirtualDisk object
type VirtualDiskReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualdisks/finalizers,verbs=update
// +kubebuilder:rbac:groups=cdi.kubevirt.io,resources=datavolumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *VirtualDiskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the VirtualDisk instance
	virtualDisk := &v1beta1.VirtualDisk{}
	err := r.Get(ctx, req.NamespacedName, virtualDisk)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("VirtualDisk resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get VirtualDisk")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if virtualDisk.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, virtualDisk)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(virtualDisk, VirtualDiskProtectionFinalizer) {
		controllerutil.AddFinalizer(virtualDisk, VirtualDiskProtectionFinalizer)
		return ctrl.Result{}, r.Update(ctx, virtualDisk)
	}

	// Reconcile the VirtualDisk
	return r.reconcileVirtualDisk(ctx, virtualDisk)
}

func (r *VirtualDiskReconciler) reconcileVirtualDisk(ctx context.Context, virtualDisk *v1beta1.VirtualDisk) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if DataVolume already exists
	dataVolumeName := virtualDisk.Name
	existingDataVolume := &cdiv1beta1.DataVolume{}
	err := r.Get(ctx, client.ObjectKey{Namespace: virtualDisk.Namespace, Name: dataVolumeName}, existingDataVolume)
	if err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to get existing DataVolume")
		return ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		// Create DataVolume
		err = r.createDataVolume(ctx, virtualDisk)
		if err != nil {
			log.Error(err, "Failed to create DataVolume")
			r.Recorder.Event(virtualDisk, "Warning", "DataVolumeCreationFailed", fmt.Sprintf("Failed to create DataVolume: %v", err))
			return ctrl.Result{}, err
		}
		log.Info("Created DataVolume", "name", dataVolumeName)
		r.Recorder.Event(virtualDisk, "Normal", "DataVolumeCreated", fmt.Sprintf("Created DataVolume %s", dataVolumeName))
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Check DataVolume status
	if !r.isDataVolumeReady(existingDataVolume) {
		log.Info("DataVolume not ready yet", "name", dataVolumeName, "phase", existingDataVolume.Status.Phase)
		return ctrl.Result{RequeueAfter: time.Second * 1}, nil
	}

	// Update VirtualDisk status
	if virtualDisk.Status.Phase != v1beta1.VirtualDiskReady {
		virtualDisk.Status.Phase = v1beta1.VirtualDiskReady
		virtualDisk.Status.Conditions = []metav1.Condition{
			{
				Type:               string(v1beta1.VirtualDiskReadyCondition),
				Status:             metav1.ConditionTrue,
				Reason:             "DataVolumeReady",
				Message:            "DataVolume is ready",
				LastTransitionTime: metav1.Now(),
			},
		}
		err = r.Status().Update(ctx, virtualDisk)
		if err != nil {
			log.Error(err, "Failed to update VirtualDisk status")
			return ctrl.Result{}, err
		}
		log.Info("VirtualDisk is ready", "name", virtualDisk.Name)
		r.Recorder.Event(virtualDisk, "Normal", "VirtualDiskReady", "VirtualDisk is ready")
	}

	return ctrl.Result{}, nil
}

func (r *VirtualDiskReconciler) createDataVolume(ctx context.Context, virtualDisk *v1beta1.VirtualDisk) error {
	// Safely get storage request with proper nil checks
	var storageRequest resource.Quantity
	if virtualDisk.Spec.Storage != nil &&
		virtualDisk.Spec.Storage.Resources.Requests != nil {
		if req, ok := virtualDisk.Spec.Storage.Resources.Requests["storage"]; ok {
			storageRequest = req
		}
	}
	volMode := &[]corev1.PersistentVolumeMode{corev1.PersistentVolumeFilesystem}[0]

	// Use default if not specified
	if storageRequest.IsZero() {
		storageRequest = resource.MustParse("70Gi")
	}
	dataVolume := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      virtualDisk.Name,
			Namespace: virtualDisk.Namespace,
			Labels:    virtualDisk.Labels,
		},
		Spec: cdiv1beta1.DataVolumeSpec{
			Storage: &cdiv1beta1.StorageSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageRequest,
					},
				},
				VolumeMode:  volMode,
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Source: r.buildDataVolumeSource(virtualDisk),
		},
	}

	// Set VirtualDisk as owner of DataVolume
	if err := controllerutil.SetControllerReference(virtualDisk, dataVolume, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	return r.Create(ctx, dataVolume)
}

func (r *VirtualDiskReconciler) buildDataVolumeSource(virtualDisk *v1beta1.VirtualDisk) *cdiv1beta1.DataVolumeSource {
	// Check if VirtualDisk has source configuration

	if virtualDisk.Spec.Source != nil {
		if virtualDisk.Spec.Source.HTTP != nil {
			return &cdiv1beta1.DataVolumeSource{
				HTTP: &cdiv1beta1.DataVolumeSourceHTTP{
					URL: virtualDisk.Spec.Source.HTTP.URL,
				},
			}
		}
		if virtualDisk.Spec.Source.Registry != nil {
			return &cdiv1beta1.DataVolumeSource{
				Registry: &cdiv1beta1.DataVolumeSourceRegistry{
					URL:        &virtualDisk.Spec.Source.Registry.URL,
					PullMethod: (*cdiv1beta1.RegistryPullMethod)(&virtualDisk.Spec.Source.Registry.PullMethod),
				},
			}
		}

		if virtualDisk.Spec.Source.DiskSnapshot != nil {

			return &cdiv1beta1.DataVolumeSource{
				Snapshot: &cdiv1beta1.DataVolumeSourceSnapshot{
					Namespace: virtualDisk.Namespace,
					Name:      virtualDisk.Spec.Source.DiskSnapshot.DiskSnapshotName,
				},
			}

		}

		if virtualDisk.Spec.Source.Empty != nil {
			return &cdiv1beta1.DataVolumeSource{
				Blank: &cdiv1beta1.DataVolumeBlankImage{},
			}
		}
	}

	// Default to HTTP source if no source is specified
	return &cdiv1beta1.DataVolumeSource{
		HTTP: &cdiv1beta1.DataVolumeSourceHTTP{
			URL: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img", // Default URL
		},
	}
}

func (r *VirtualDiskReconciler) isDataVolumeReady(dataVolume *cdiv1beta1.DataVolume) bool {
	return dataVolume.Status.Phase == cdiv1beta1.Succeeded
}

func (r *VirtualDiskReconciler) handleDeletion(ctx context.Context, virtualDisk *v1beta1.VirtualDisk) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if DataVolume exists and delete it
	dataVolumeName := virtualDisk.Name
	dataVolume := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataVolumeName,
			Namespace: virtualDisk.Namespace,
		},
	}

	err := r.Delete(ctx, dataVolume)
	if err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to delete DataVolume", "name", dataVolumeName)
		return ctrl.Result{}, err
	}

	if err == nil {
		log.Info("Deleted DataVolume", "name", dataVolumeName)
		r.Recorder.Event(virtualDisk, "Normal", "DataVolumeDeleted", fmt.Sprintf("Deleted DataVolume %s", dataVolumeName))
	}

	// Remove finalizer after DataVolume is deleted
	if controllerutil.ContainsFinalizer(virtualDisk, VirtualDiskProtectionFinalizer) {
		controllerutil.RemoveFinalizer(virtualDisk, VirtualDiskProtectionFinalizer)
		if err := r.Update(ctx, virtualDisk); err != nil {
			return ctrl.Result{}, err
		}
	}

	log.Info("VirtualDisk deleted", "name", virtualDisk.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualDiskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualDisk{}).
		Owns(&cdiv1beta1.DataVolume{}).
		Named("virtualdisk").
		Complete(r)
}
