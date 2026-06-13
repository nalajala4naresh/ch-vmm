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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nalajala4naresh/ch-vmm/pkg/cloudutils"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const SnapShotFinalizer = "cloudhypervisor.quill.today/vmsnapshot-protection"

// VMSnapShotReconciler reconciles a VMSnapShot object
type VMSnapShotReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmsnapshots/finalizers,verbs=update
func (s *VMSnapShotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	l.Info("reconciling for snapshot", "snapshot", req.NamespacedName)

	// Get the snapshot object if the snapshot object not found, skip processing event
	var snap v1beta1.VMSnapShot

	if err := s.Get(ctx, req.NamespacedName, &snap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	//Add finalizer to do cleanup
	if snap.DeletionTimestamp == nil && !controllerutil.ContainsFinalizer(&snap, SnapShotFinalizer) {
		controllerutil.AddFinalizer(&snap, SnapShotFinalizer)
		err := s.Client.Update(ctx, &snap)
		if err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			} else {
				return ctrl.Result{}, fmt.Errorf("unable to add finalizer to snapshot object : %s", err)
			}

		}
		return ctrl.Result{}, nil

	}

	if snap.DeletionTimestamp != nil && !snap.DeletionTimestamp.IsZero() {

		if snap.Status.VMSnapShotStatusPhase == v1beta1.VMSnapShotInProgress || snap.Status.VMSnapShotStatusPhase == v1beta1.VMSnapShotScheduled {

			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil

		}
		// always delete the snapshot from object store or localpath
		err := s.deleteSnapshot(ctx, snap)
		if err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
		}
		controllerutil.RemoveFinalizer(&snap, SnapShotFinalizer)
		err = s.Client.Update(ctx, &snap)
		if err != nil {

			return ctrl.Result{}, fmt.Errorf("unable to remove finalizer: %s", err)

		}
		return ctrl.Result{}, nil
	}

	if snap.Status.VMSnapShotStatusPhase == "" {
		vmName := snap.Spec.VmName

		vmkey := types.NamespacedName{Namespace: req.Namespace, Name: vmName}

		var vm v1beta1.VirtualMachine
		if err := s.Get(ctx, vmkey, &vm); err != nil {
			if apierrors.IsNotFound(err) {
				s.Recorder.Eventf(&vm, corev1.EventTypeWarning, "VMNotFound", "VM not found")
			}
			snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapshotFailed

		} else {

			snap.Status.VMSnapShotStatusPhase = v1beta1.VMSnapShotScheduled

		}

		// if vm is found update the snapshot status to scheduled

		if err := s.Status().Update(ctx, &snap); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 30}, nil
			} else {
				return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 30}, err
			}
		}

	}
	return ctrl.Result{}, nil

}

func (s *VMSnapShotReconciler) deleteSnapshot(ctx context.Context, snap v1beta1.VMSnapShot) error {

	var err error

	//delete memory snapshot
	if !snap.Spec.SkipMemorySnapshot {

		memeorySnapName := snap.Name + ".zstd.tar"
		bucket := snap.Spec.Bucket
		if strings.HasPrefix(bucket, "s3") {
			bucket = strings.TrimPrefix(bucket, "s3://")
			err = cloudutils.DeleteObjectFromS3(bucket, snap.Status.MemoryStatus.ObjectKey)

		} else {
			bucket = strings.TrimPrefix(bucket, "gcs://")
			err = cloudutils.DeleteObjectFromGCS(bucket, snap.Status.MemoryStatus.ObjectKey)
		}

		if err == nil {
			s.Recorder.Eventf(&snap, corev1.EventTypeNormal, "MemorySnapshotDeleted", "Memory snapshot deleted %s", memeorySnapName)

		} else {

			s.Recorder.Eventf(&snap, corev1.EventTypeWarning, "MemorySnapshotDeleteFailed", "Memory snapshot delete failed for %s", memeorySnapName)
			return err

		}

	}

	//delete disk snapshots by deleting VirtualDiskSnapshot resources
	for _, disk := range snap.Status.DiskStatus {

		vds := &v1beta1.VirtualDiskSnapshot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      disk.Name,
				Namespace: snap.Namespace,
			},
		}

		err := s.Delete(ctx, vds)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// VirtualDiskSnapshot was already deleted, log and continue
				log.FromContext(ctx).Info("VirtualDiskSnapshot already deleted", "name", disk.Name)
				s.Recorder.Eventf(&snap, corev1.EventTypeNormal, "VirtualDiskSnapshotDeleted", "VirtualDiskSnapshot already deleted %s", disk.Name)
				continue
			} else {
				s.Recorder.Eventf(&snap, corev1.EventTypeWarning, "VirtualDiskSnapshotDeleteFailed", "Failed to delete VirtualDiskSnapshot %s: %v", disk.Name, err)
				return err
			}
		}

		s.Recorder.Eventf(&snap, corev1.EventTypeNormal, "VirtualDiskSnapshotDeleted", "VirtualDiskSnapshot deleted %s", disk.Name)

	}

	return nil

}

// SetupWithManager sets up the controller with the Manager.
func (s *VMSnapShotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log.Log.Info("Setting up VMSnapShot controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VMSnapShot{}).
		Named("vmsnapshot-controller").
		Complete(s)
}
