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
	"reflect"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nalajala4naresh/ch-vmm/pkg/volumeutil"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

// VMRollbackReconciler reconciles a VMRollback object
type VMRollbackReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmrollbacks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmrollbacks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=vmrollbacks/finalizers,verbs=update
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VMRollback object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *VMRollbackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	_ = log.FromContext(ctx)
	var vr v1beta1.VMRollback

	// if VMRollback is not found in etcd, skip this event
	if err := r.Get(ctx, req.NamespacedName, &vr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)

	}

	status := vr.Status.DeepCopy()
	var rerr error
	switch vr.Status.Phase {
	case v1beta1.RollbackScheduled, v1beta1.RollbackFailed:
		rerr = r.reconcile(ctx, vr, vr.Spec.Snapshot)

	case v1beta1.RollbackSucceeded:

	case "":
		vr.Status.Phase = v1beta1.RollbackScheduled

	}

	if !reflect.DeepEqual(status, vr.Status) {

		if err := r.Status().Update(ctx, &vr); err != nil {
			//if the vm snapshot already exists
			if client.IgnoreAlreadyExists(rerr) == nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("unable to update VMRollback object status : %s", err)
			}
		}

	}

	return ctrl.Result{}, nil

}

func (r *VMRollbackReconciler) reconcile(ctx context.Context, vr v1beta1.VMRollback, snapshot string) error {

	var vs v1beta1.VMSnapShot
	snapshotName := types.NamespacedName{Namespace: vr.Namespace, Name: snapshot}

	//fetch the backing snapshot for the rollback & if not found fail the process
	if err := r.Get(ctx, snapshotName, &vs); err != nil {
		vr.Status.Phase = v1beta1.RollbackFailed
		r.Recorder.Eventf(&vr, corev1.EventTypeWarning, "MissingSnapshot", fmt.Sprintf("Snapshot missing %q", vr.Name))
		if err := r.Status().Update(ctx, &vr); err != nil {

			return fmt.Errorf("unable to update VMRollback status : %s", err)
		}

		return nil

	}

	//after getting the snapshot fetch vmrestore spec with same name
	var vrs v1beta1.VMRestoreSpec
	vrsName := types.NamespacedName{Namespace: vr.Namespace, Name: vr.Spec.Snapshot}
	if err := r.Get(ctx, vrsName, &vrs); err != nil {
		vr.Status.Phase = v1beta1.RollbackFailed
		r.Recorder.Eventf(&vr, corev1.EventTypeWarning, "MissingRestoreSpec", fmt.Sprintf("VMRestoreSpec missing %q", vr.Name))
		if err := r.Status().Update(ctx, &vr); err != nil {

			return fmt.Errorf("unable to update VMRollback status : %s", err)
		}

		return nil

	}

	//now you have the data to work with and create PVC's from the snapshot and create the VM object using those PVC's for the disk
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ergp, gctx := errgroup.WithContext(tctx)
	var volumes []*v1beta1.Volume
	var mu sync.Mutex
	var namespace string
	if vs.Namespace == "" {
		namespace = "default"
	} else {
		namespace = vs.Namespace

	}
	for _, disk := range vs.Status.DiskStatus {

		//check if the k8s volumesnapshot is ready to use

		ergp.Go(func() error {
			err := volumeutil.RecoverSnapShotToPVC(gctx, r.Client, disk.SnapshotName, namespace, disk.SnapshotName, "")
			vol := &v1beta1.Volume{Name: disk.Name,
				VolumeSource: v1beta1.VolumeSource{PersistentVolumeClaim: &v1beta1.PersistentVolumeClaimVolumeSource{ClaimName: disk.SnapshotName}}}
			if err != nil {
				r.Recorder.Eventf(&vr, corev1.EventTypeWarning, "RecoverSnapShotToPVCFailed", fmt.Sprintf("pvc restoration from snapshot failed %q with error %s", disk.Name, err))
				return err
			}
			r.Recorder.Eventf(&vr, corev1.EventTypeNormal, "RecoverSnapShotToPVCSucceeded", fmt.Sprintf("pvc restoration from snapshot succeeded %q", disk.Name))
			mu.Lock()
			volumes = append(volumes, vol)
			mu.Unlock()
			return nil
		})

	}

	if err := ergp.Wait(); err != nil {
		vr.Status.Phase = v1beta1.RollbackFailed

		return err
	}

	//now get the non-pvc based  volumes and append to the new vm we are creating.
	for _, vol := range vrs.Spec.VMSpec.Volumes {
		if vol.DataVolume != nil || vol.PersistentVolumeClaim != nil {
			continue
		}
		volumes = append(volumes, vol)
	}
	memVol := &v1beta1.Volume{Name: "memory", VolumeSource: v1beta1.VolumeSource{MemorySnapshot: &v1beta1.MemorySnapshotSource{Bucket: vs.Status.MemoryStatus.Bucket, Key: vs.Status.MemoryStatus.ObjectKey}}}

	volumes = append(volumes, memVol)

	//now create VM object
	vm := &v1beta1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vs.Spec.VmName + "-" + snapshotName.Name,
			Namespace: namespace,
		},
		Spec: v1beta1.VirtualMachineSpec{
			Instance:  vrs.Spec.VMSpec.Instance,
			Resources: vrs.Spec.VMSpec.Resources,
			RunPolicy: vrs.Spec.VMSpec.RunPolicy,
			Networks:  vrs.Spec.VMSpec.Networks,
			Volumes:   volumes,
		},
	}

	// post the object to K8S
	rerr := r.Client.Create(ctx, vm)
	if rerr != nil && client.IgnoreAlreadyExists(rerr) == nil {
		vr.Status.Phase = v1beta1.RollbackSucceeded
		return nil
	}
	vr.Status.Phase = v1beta1.RollbackFailed
	return rerr

}

// SetupWithManager sets up the controller with the Manager.
func (r *VMRollbackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VMRollback{}).
		Named("vmrollback").
		Complete(r)
}
