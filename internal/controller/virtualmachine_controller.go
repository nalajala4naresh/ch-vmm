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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nalajala4naresh/ch-vmm/pkg/volumeutil"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	VMProtectionFinalizer = "cloudhypervisor.quill.today/vm-protection"
)

// VirtualMachineReconciler reconciles a VirtualMachine object
type VirtualMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder           record.EventRecorder
	PrerunnerImageName string
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods/resize,verbs=update
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=cdi.kubevirt.io,resources=datavolumes,verbs=get;list;watch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *VirtualMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var vm v1beta1.VirtualMachine

	// if VM is not found in etcd, skip this event
	if err := r.Get(ctx, req.NamespacedName, &vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)

	}

	// add finalizer if the vm is not deleted.
	if vm.DeletionTimestamp == nil && !controllerutil.ContainsFinalizer(&vm, VMProtectionFinalizer) {
		controllerutil.AddFinalizer(&vm, VMProtectionFinalizer)
		err := r.Client.Update(ctx, &vm)
		if err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			} else {
				return ctrl.Result{}, fmt.Errorf("unable to add finalizer: %s", err)
			}

		}
		return ctrl.Result{}, nil
	}
	//Handle if the vm is deleted and VM is not in Running Phase
	// if VM is already running , then daemon should delete the VM.
	if vm.DeletionTimestamp != nil && !vm.ObjectMeta.DeletionTimestamp.IsZero() {
		if vm.Status.Phase == "" ||
			vm.Status.Phase == v1beta1.VirtualMachinePending ||
			vm.Status.Phase == v1beta1.VirtualMachineScheduling ||
			vm.Status.Phase == v1beta1.VirtualMachineFailed ||
			vm.Status.Phase == v1beta1.VirtualMachineSucceeded {

			// delete all pods tied to this VM
			deleted, err := r.deleteAllVMPods(ctx, &vm)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("unable to delete pods for VM delete: %s", err)
			}
			if deleted {
				controllerutil.RemoveFinalizer(&vm, VMProtectionFinalizer)
				err = r.Client.Update(ctx, &vm)
				if err != nil {

					return ctrl.Result{}, fmt.Errorf("unable to remove finalizer: %s", err)

				}
				return ctrl.Result{}, nil

			}

		}
	}

	status := vm.Status.DeepCopy()

	// actual reconcile logic
	rerr := r.reconcile(ctx, &vm)

	if !reflect.DeepEqual(vm.Status, status) {
		if err := r.Status().Update(ctx, &vm); err != nil {
			if rerr == nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("unable to update VM status : %s", err)
			}
		}
	}

	if rerr != nil {
		reconcileErr := reconcileError{}
		if errors.As(rerr, &reconcileErr) {
			return reconcileErr.Result, nil
		}

		r.Recorder.Eventf(&vm, corev1.EventTypeWarning, "FailedReconcile", "Failed to reconcile VM: %s", rerr)
		return ctrl.Result{}, rerr
	}

	if err := r.gcVMPods(ctx, &vm); err != nil {
		return ctrl.Result{}, fmt.Errorf("GC VM Pods: %s", err)
	}

	return ctrl.Result{}, nil

}

func (r *VirtualMachineReconciler) gcVMPods(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	//During Every reconcile loop we will check if the old pods are lingering around for a given VM and clean them up
	var vmPodList corev1.PodList
	if err := r.List(ctx, &vmPodList, client.MatchingFields{"vmUID": string(vm.UID)}); err != nil {
		return fmt.Errorf("list VM Pods: %s", err)
	}

	for _, vmPod := range vmPodList.Items {
		//pod is already scheduled to be deleted
		if vmPod.DeletionTimestamp != nil && !vmPod.DeletionTimestamp.IsZero() {
			continue
		}

		//if the pod actually belongs to the VM and pod belongs to target pod during migration then skip
		if vmPod.Name == vm.Status.VMPodName || (vm.Status.Migration != nil && vmPod.Name == vm.Status.Migration.TargetVMPodName) {
			continue
		}

		//If not Delete the old (stale) pod
		if err := r.Delete(ctx, &vmPod); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete VM Pod: %s", err)
		}
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "GCDeletedVMPod", fmt.Sprintf("Deleted VM Pod %q", vmPod.Name))
	}
	return nil
}

func (r *VirtualMachineReconciler) reconcile(ctx context.Context, vm *v1beta1.VirtualMachine) error {

	// switch on the phase of the VM and take actions
	switch vm.Status.Phase {
	case v1beta1.VirtualMachinePending:
		vm.Status.VMPodName = names.SimpleNameGenerator.GenerateName(fmt.Sprintf("vm-%s-", vm.Name))

		vm.Status.Phase = v1beta1.VirtualMachineScheduling

	case v1beta1.VirtualMachineScheduling, v1beta1.VirtualMachineScheduled:

		var vmPod corev1.Pod
		vmPodKey := types.NamespacedName{
			Name:      vm.Status.VMPodName,
			Namespace: vm.Namespace,
		}
		if vmPodKey.Namespace == "" {
			vmPodKey.Namespace = "default"
		}
		vmPodNotFound := false
		if err := r.Get(ctx, vmPodKey, &vmPod); err != nil {
			if apierrors.IsNotFound(err) {
				ctrl.Log.Info("vmpod not found and status is scheduling/scheduled", "vmPod", vmPod, "status", vm.Status.Phase)
				vmPodNotFound = true
			} else {
				return fmt.Errorf("get VM Pod: %s", err)
			}
		}

		if !vmPodNotFound && !metav1.IsControlledBy(&vmPod, vm) {
			vmPodNotFound = true
		}

		if vmPodNotFound {
			if vm.Status.Phase == v1beta1.VirtualMachineScheduling {
				vmPod, err := r.buildVMPod(ctx, vm)
				if err != nil {
					ctrl.Log.Error(err, "Unable to build VM Pod", "vmPod", vmPod, "status", vm.Status.Phase, "vm", vm.Name)
					return fmt.Errorf("build VM Pod: %w", err)
				}

				vmPod.ObjectMeta.Name = vmPodKey.Name
				if vmPodKey.Namespace == "" {
					vmPod.ObjectMeta.Namespace = "default"
				} else {
					vmPod.ObjectMeta.Namespace = vmPodKey.Namespace

				}

				if err := controllerutil.SetControllerReference(vm, vmPod, r.Scheme); err != nil {
					return fmt.Errorf("set VM Pod controller reference: %s", err)
				}

				if err := r.Create(ctx, vmPod); err != nil {
					data, _ := json.Marshal(vmPod)
					ctrl.Log.Error(err, "Unable to Create VM Pod", "vmdata", string(data), "status", vm.Status.Phase, "vm", vm.Name)
					return fmt.Errorf("create VM Pod: %s", err)
				}
				r.Recorder.Eventf(vm, corev1.EventTypeNormal, "CreatedVMPod", "Created VM Pod %q", vmPod.Name)
			} else {
				vm.Status.Phase = v1beta1.VirtualMachineFailed
			}
		} else {
			vm.Status.VMPodUID = vmPod.UID
			vm.Status.NodeName = vmPod.Spec.NodeName

			if vmPod.Spec.NodeName != "" {
				if err := r.handleHotplugVolumes(ctx, vm, &vmPod, true); err != nil {
					ctrl.Log.Error(err, "Unable to handle hot plug volume", "vmPod", vmPod, "status", vm.Status.Phase, "vm", vm.Name)
					return err
				}
			}

			allHotplugVolumesAttached := true
			for _, volume := range vm.Spec.Volumes {
				if !volume.IsHotpluggable() {
					continue
				}
				var volumeStatus *v1beta1.VolumeStatus
				for i := range vm.Status.VolumeStatus {
					if vm.Status.VolumeStatus[i].Name == volume.Name {
						volumeStatus = &vm.Status.VolumeStatus[i]
					}
				}
				if volumeStatus == nil || volumeStatus.Phase != v1beta1.VolumeAttachedToNode {
					allHotplugVolumesAttached = false
				}
			}

			switch vmPod.Status.Phase {
			case corev1.PodRunning:
				if vm.Status.Phase == v1beta1.VirtualMachineScheduling && allHotplugVolumesAttached {
					vm.Status.Phase = v1beta1.VirtualMachineScheduled
				}
			case corev1.PodSucceeded:
				vm.Status.Phase = v1beta1.VirtualMachineSucceeded
			case corev1.PodFailed:
				vm.Status.Phase = v1beta1.VirtualMachineFailed
			case corev1.PodUnknown:
				vm.Status.Phase = v1beta1.VirtualMachineUnknown
			default:
				// ignored
			}
			if err := r.updateHotplugVolumeStatus(ctx, vm, &vmPod); err != nil {
				ctrl.Log.Error(err, "Unable to update hot plug volume status", "vmPod", vmPod, "status", vm.Status.Phase, "vm", vm.Name)
				return err
			}
		}
	case v1beta1.VirtualMachinePodResizeInProgress:
		// Handle VM resize in progress
		// Resize Pod resources, then wait for Pod resize to complete, then resize VM
		if err := r.reconcileVMPodResize(ctx, vm); err != nil {
			return fmt.Errorf("reconcile VM resize: %w", err)
		}

	case v1beta1.VirtualMachineRunning:
		var vmPod corev1.Pod
		vmPodKey := types.NamespacedName{
			Name:      vm.Status.VMPodName,
			Namespace: vm.Namespace,
		}
		if vmPodKey.Namespace == "" {
			vmPodKey.Namespace = "default"
		}
		vmPodNotFound := false
		if err := r.Get(ctx, vmPodKey, &vmPod); err != nil {
			if apierrors.IsNotFound(err) {
				vmPodNotFound = true
			} else {
				return fmt.Errorf("get VM Pod: %s", err)
			}
		}

		if !vmPodNotFound && !metav1.IsControlledBy(&vmPod, vm) {
			vmPodNotFound = true
		}
		switch {
		case vmPodNotFound:
			vm.Status.Phase = v1beta1.VirtualMachineFailed
		case vmPod.Status.Phase == corev1.PodSucceeded:
			if vm.Status.Migration == nil {
				vm.Status.Phase = v1beta1.VirtualMachineSucceeded
			}
		case vmPod.Status.Phase == corev1.PodFailed:
			vm.Status.Phase = v1beta1.VirtualMachineFailed
		case vmPod.Status.Phase == corev1.PodUnknown:
			vm.Status.Phase = v1beta1.VirtualMachineUnknown
		}
		if vm.Status.Phase != v1beta1.VirtualMachineRunning {
			return nil
		}

		// Check for CPU/memory changes by comparing with Pod actual resources
		if !vmPodNotFound {
			if err := r.checkAndSetResizePhase(ctx, vm, &vmPod); err != nil {
				return err
			}
		}

		if err := r.reconcileVMConditions(ctx, vm, &vmPod); err != nil {
			ctrl.Log.Error(err, "Failed to reconcile vm condtions", "vm", vm.Name, "status", vm.Status.Phase, "vm", vm.Name)
			return err
		}

		if vm.Status.Migration != nil {
			switch vm.Status.Migration.Phase {
			case "", v1beta1.VirtualMachineMigrationPending:
				vm.Status.Migration.TargetVMPodName = names.SimpleNameGenerator.GenerateName(fmt.Sprintf("vm-%s-", vm.Name))
				vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationScheduling
			case v1beta1.VirtualMachineMigrationScheduling:
				var targetVMPod corev1.Pod
				targetVMPodKey := types.NamespacedName{
					Name:      vm.Status.Migration.TargetVMPodName,
					Namespace: vm.Namespace,
				}
				if targetVMPodKey.Namespace == "" {
					targetVMPodKey.Namespace = "default"
				}
				targetVMPodNotFound := false
				if err := r.Get(ctx, targetVMPodKey, &targetVMPod); err != nil {
					if apierrors.IsNotFound(err) {
						vmPodNotFound = true
					} else {
						return fmt.Errorf("get target VM Pod: %s", err)
					}
				}

				if !targetVMPodNotFound && !metav1.IsControlledBy(&targetVMPod, vm) {
					targetVMPodNotFound = true
				}

				if targetVMPodNotFound {
					targetVMPod, err := r.buildTargetVMPod(ctx, vm)
					if err != nil {
						return fmt.Errorf("build target VM Pod: %s", err)
					}

					targetVMPod.Name = targetVMPodKey.Name
					targetVMPod.Namespace = targetVMPodKey.Namespace
					if err := controllerutil.SetControllerReference(vm, targetVMPod, r.Scheme); err != nil {
						ctrl.Log.Error(err, "Failed to set Controller reference", "vmPod", vmPod, "status", vm.Status.Phase, "vm", vm.Name)
						return fmt.Errorf("set target VM Pod controller reference: %s", err)
					}
					if err := r.Create(ctx, targetVMPod); err != nil {
						return fmt.Errorf("create target VM Pod: %s", err)
					}
					r.Recorder.Eventf(vm, corev1.EventTypeNormal, "CreatedTargetVMPod", "Created target VM Pod %q", targetVMPod.Name)
				} else {
					vm.Status.Migration.TargetVMPodUID = targetVMPod.UID
					vm.Status.Migration.TargetNodeName = targetVMPod.Spec.NodeName

					isHotplugVolumesAttachedToTargetNode := false
					if targetVMPod.Status.Phase == corev1.PodFailed || targetVMPod.Status.Phase == corev1.PodSucceeded || targetVMPod.Status.Phase == corev1.PodUnknown {
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
					} else if len(getHotplugVolumes(vm, &targetVMPod)) == 0 {
						isHotplugVolumesAttachedToTargetNode = true
					} else if targetVMPod.Spec.NodeName != "" {
						// Clear volume status to make volume be mounted in target volume pod.
						vmCopy := vm.DeepCopy()
						vmCopy.Status.VolumeStatus = []v1beta1.VolumeStatus{}
						if err := r.handleHotplugVolumes(ctx, vmCopy, &targetVMPod, true); err != nil {
							return err
						}
						targetVolumePods, err := r.getHotplugVolumePods(ctx, &targetVMPod)
						if err != nil {
							return err
						}
						if len(targetVolumePods) > 0 {
							vm.Status.Migration.TargetVolumePodUID = targetVolumePods[0].UID
							switch targetVolumePods[0].Status.Phase {
							case corev1.PodFailed, corev1.PodSucceeded, corev1.PodUnknown:
								vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
							case corev1.PodRunning:
								isHotplugVolumesAttachedToTargetNode = true
							}
						}
					}

					if targetVMPod.Status.Phase == corev1.PodRunning && isHotplugVolumesAttachedToTargetNode {
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationScheduled
					}
				}
			}
		} else {
			if err := r.handleHotplugVolumes(ctx, vm, &vmPod, false); err != nil {
				ctrl.Log.Error(err, "Failed to Handle hot plug volumes", "vmPod", vmPod, "vm", vm.Name, "status", vm.Status.Phase)
				return err
			}
			if err := r.updateHotplugVolumeStatus(ctx, vm, &vmPod); err != nil {
				ctrl.Log.Error(err, "Failed to update hot plug volume status", "vmPod", vmPod, "vm", vm.Name, "status", vm.Status.Phase)
				return err
			}
		}
	case "", v1beta1.VirtualMachineSucceeded, v1beta1.VirtualMachineFailed:
		deleted, err := r.deleteAllVMPods(ctx, vm)
		if err != nil {
			return err
		}
		if !deleted {

			return nil
		}

		run := false
		switch vm.Spec.RunPolicy {
		case v1beta1.RunAlways:
			run = true
		case v1beta1.RunPolicyRerunOnFailure:
			run = vm.Status.Phase == v1beta1.VirtualMachineFailed || vm.Status.Phase == "" || vm.Status.PowerAction == v1beta1.VirtualMachinePowerOn
		case v1beta1.RunPolicyOnce:
			run = vm.Status.Phase == "" || vm.Status.PowerAction == v1beta1.VirtualMachinePowerOn
		case v1beta1.RunPolicyManual:
			run = vm.Status.PowerAction == v1beta1.VirtualMachinePowerOn
		default:
			// ignored
		}

		if run {
			vm.Status.Phase = v1beta1.VirtualMachinePending
		}

	default:
		// ignored
	}
	return nil

}

// checkAndSetResizePhase detects CPU/memory changes by comparing VM spec with Pod actual resources
// Uses Pod status.containerStatuses[].resources (K8s 1.35+) to get actual running resources
func (r *VirtualMachineReconciler) checkAndSetResizePhase(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod) error {

	// Only check if VM is in Running phase
	if vm.Status.Phase != v1beta1.VirtualMachineRunning {
		return nil
	}

	// Skip if already in resize phase
	if vm.Status.Phase == v1beta1.VirtualMachinePodResizeInProgress {
		return nil
	}

	// Get actual resources from Pod status (K8s 1.35+ feature)
	var actualResources *corev1.ResourceRequirements
	for i := range vmPod.Status.ContainerStatuses {
		if vmPod.Status.ContainerStatuses[i].Name == "vm-manager" {
			if vmPod.Status.ContainerStatuses[i].Resources != nil {
				actualResources = vmPod.Status.ContainerStatuses[i].Resources
				break
			}
		}
	}

	if actualResources == nil {
		return errors.New("k8s is not 1.35 version and ContainerStatuses does not contain Resources")
	}

	// Calculate desired resources from VM spec
	desiredCPU := resource.NewQuantity(int64(vm.Spec.Instance.CPU.Sockets*vm.Spec.Instance.CPU.CoresPerSocket), resource.DecimalSI)
	memOverhead := resource.MustParse("256Mi")
	desiredMem := vm.Spec.Instance.Memory.Size.DeepCopy()
	desiredMem.Add(memOverhead)

	// Get actual resources from Pod
	actualCPU := actualResources.Requests[corev1.ResourceCPU]
	actualMem := actualResources.Requests[corev1.ResourceMemory]

	// Check if CPU or memory changed
	cpuChanged := actualCPU.IsZero() || !actualCPU.Equal(*desiredCPU)
	memoryChanged := actualMem.IsZero() || !actualMem.Equal(desiredMem)

	if cpuChanged || memoryChanged {
		vm.Status.Phase = v1beta1.VirtualMachinePodResizeInProgress
		ctrl.Log.Info("VM resize initiated: Pod actual resources differ from VM spec",
			"vm", vm.Name,
			"cpuChanged", cpuChanged,
			"memoryChanged", memoryChanged,
			"actualCPU", actualCPU.String(),
			"desiredCPU", desiredCPU.String(),
			"actualMem", actualMem.String(),
			"desiredMem", desiredMem.String())

		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResizeInitiated",
			"VM resize initiated: CPU=%s->%s, Memory=%s->%s",
			actualCPU.String(), desiredCPU.String(),
			actualMem.String(), desiredMem.String())
	}

	return nil
}

func (r *VirtualMachineReconciler) reconcileVMConditions(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod) error {
	for _, condition := range vmPod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			readyCondition := metav1.Condition{
				Type:    string(v1beta1.VirtualMachineReady),
				Status:  metav1.ConditionStatus(condition.Status),
				Reason:  condition.Reason,
				Message: condition.Message,
			}
			if readyCondition.Reason == "" {
				readyCondition.Reason = string(readyCondition.Status)
			}
			meta.SetStatusCondition(&vm.Status.Conditions, readyCondition)
		}
	}

	if meta.FindStatusCondition(vm.Status.Conditions, string(v1beta1.VirtualMachineMigratable)) == nil {
		migratableCondition, err := r.calculateMigratableCondition(ctx, vm)
		if err != nil {
			return fmt.Errorf("calculate VM migratable condition: %s", err)
		}
		meta.SetStatusCondition(&vm.Status.Conditions, *migratableCondition)
	}
	return nil
}

// reconcileVMResize handles the VMResizeInProgress phase
// It resizes the Pod, waits for Pod resize to complete, then the daemon will resize the VM
func (r *VirtualMachineReconciler) reconcileVMPodResize(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	var vmPod corev1.Pod
	vmPodKey := types.NamespacedName{
		Name:      vm.Status.VMPodName,
		Namespace: vm.Namespace,
	}
	if vmPodKey.Namespace == "" {
		vmPodKey.Namespace = "default"
	}

	if err := r.Get(ctx, vmPodKey, &vmPod); err != nil {
		if apierrors.IsNotFound(err) {
			vm.Status.Phase = v1beta1.VirtualMachineFailed
			return nil
		}
		return fmt.Errorf("get VM Pod: %w", err)
	}

	// Check Pod resize status
	if vmPod.Status.Resize == corev1.PodResizeStatusInProgress {
		ctrl.Log.Info("VM resize: Pod resize in progress, waiting",
			"vm", vm.Name, "pod", vmPod.Name)

		return nil
	}

	if vmPod.Status.Resize == corev1.PodResizeStatusInfeasible {
		ctrl.Log.Info("VM resize: Pod resize infeasible",
			"vm", vm.Name, "pod", vmPod.Name)
		r.Recorder.Eventf(vm, corev1.EventTypeWarning, "VMPodResizeInfeasible",
			"Pod resize is infeasible, cannot resize VM")
		// Transition back to Running
		vm.Status.Phase = v1beta1.VirtualMachineRunning
		return nil
	}

	// Calculate desired Pod resources from VM spec
	desiredCPU := resource.NewQuantity(int64(vm.Spec.Instance.CPU.Sockets*vm.Spec.Instance.CPU.CoresPerSocket), resource.DecimalSI)

	// Calculate memory: VM memory + overhead
	memOverhead := resource.MustParse("256Mi")
	desiredMem := vm.Spec.Instance.Memory.Size.DeepCopy()
	desiredMem.Add(memOverhead)

	// Get current Pod resources (use actual if available, otherwise spec)
	var currentResources *corev1.ResourceRequirements
	for i := range vmPod.Status.ContainerStatuses {
		if vmPod.Status.ContainerStatuses[i].Name == "vm-manager" {
			if vmPod.Status.ContainerStatuses[i].Resources != nil {
				currentResources = vmPod.Status.ContainerStatuses[i].Resources
				break
			}
		}
	}

	// Fallback to spec if no actual resources
	if currentResources == nil {
		for i := range vmPod.Spec.Containers {
			if vmPod.Spec.Containers[i].Name == "vm-manager" {
				currentResources = &vmPod.Spec.Containers[i].Resources
				break
			}
		}
	}

	if currentResources == nil {
		return nil
	}

	// Check if Pod resources need to be updated
	needsPodResize := false
	currentCPU := currentResources.Requests[corev1.ResourceCPU]
	currentMem := currentResources.Requests[corev1.ResourceMemory]

	if currentCPU.IsZero() || !currentCPU.Equal(*desiredCPU) {
		needsPodResize = true
	}

	if currentMem.IsZero() || !currentMem.Equal(desiredMem) {
		needsPodResize = true
	}

	if needsPodResize {
		// Use Pod resize subresource (Kubernetes 1.35+)
		// Create a Pod with only the container resources we want to update
		podResizePatch := vmPod.DeepCopy()

		// Find and update the vm-manager container resources
		for i := range podResizePatch.Spec.Containers {
			if podResizePatch.Spec.Containers[i].Name == "vm-manager" {
				if podResizePatch.Spec.Containers[i].Resources.Requests == nil {
					podResizePatch.Spec.Containers[i].Resources.Requests = make(corev1.ResourceList)
				}
				if podResizePatch.Spec.Containers[i].Resources.Limits == nil {
					podResizePatch.Spec.Containers[i].Resources.Limits = make(corev1.ResourceList)
				}

				podResizePatch.Spec.Containers[i].Resources.Requests[corev1.ResourceCPU] = *desiredCPU
				podResizePatch.Spec.Containers[i].Resources.Requests[corev1.ResourceMemory] = desiredMem
				podResizePatch.Spec.Containers[i].Resources.Limits[corev1.ResourceCPU] = *desiredCPU
				podResizePatch.Spec.Containers[i].Resources.Limits[corev1.ResourceMemory] = desiredMem
				break
			}
		}

		// Use resize subresource via SubResource client
		if err := r.SubResource("resize").Update(ctx, podResizePatch, &client.SubResourceUpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				// Conflict is expected, will be retried
				return nil
			}
			r.Recorder.Eventf(vm, corev1.EventTypeWarning, "VMResizeFailed",
				"Failed to resize Pod via resize subresource: %s", err)
			return fmt.Errorf("resize Pod for VM resize: %w", err)
		}

		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResizePodInitiated",
			"Pod resize initiated via resize subresource: CPU=%s, Memory=%s", desiredCPU.String(), desiredMem.String())
		return nil
	}

	// Check if Pod resize is complete by comparing actual resources
	var actualResources *corev1.ResourceRequirements
	for i := range vmPod.Status.ContainerStatuses {
		if vmPod.Status.ContainerStatuses[i].Name == "vm-manager" {
			if vmPod.Status.ContainerStatuses[i].Resources != nil {
				actualResources = vmPod.Status.ContainerStatuses[i].Resources
				break
			}
		}
	}

	if actualResources == nil {
		// No actual resources yet, wait
		return nil
	}

	// Verify actual resources match desired
	actualCPU := actualResources.Requests[corev1.ResourceCPU]
	actualMem := actualResources.Requests[corev1.ResourceMemory]

	if actualCPU.Equal(*desiredCPU) && actualMem.Equal(desiredMem) {
		// Pod resize is complete, transition back to Running
		// The daemon will detect the change and resize the VM
		vm.Status.Phase = v1beta1.VirtualMachineResizeInProgress
		ctrl.Log.Info("VM resize: Pod resize completed, transitioning to Running",
			"vm", vm.Name)
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResizeInProgress",
			"Pod resize completed, VM resize will be handled by daemon")
	}

	return nil
}

func (r *VirtualMachineReconciler) calculateMigratableCondition(ctx context.Context, vm *v1beta1.VirtualMachine) (*metav1.Condition, error) {
	if vm.Spec.Instance.CPU.DedicatedCPUPlacement {
		return &metav1.Condition{
			Type:    string(v1beta1.VirtualMachineMigratable),
			Status:  metav1.ConditionFalse,
			Reason:  "CPUNotMigratable",
			Message: "migration is disabled when VM has enabled dedicated CPU placement",
		}, nil
	}

	for _, network := range vm.Spec.Networks {
		for _, iface := range vm.Spec.Instance.Interfaces {
			if iface.Name != network.Name {
				continue
			}
			if network.Pod != nil && iface.Bridge != nil {
				return &metav1.Condition{
					Type:    string(v1beta1.VirtualMachineMigratable),
					Status:  metav1.ConditionFalse,
					Reason:  "InterfaceNotMigratable",
					Message: "migration is disabled when VM has a bridged interface to the pod network",
				}, nil
			}
			if iface.SRIOV != nil {
				return &metav1.Condition{
					Type:    string(v1beta1.VirtualMachineMigratable),
					Status:  metav1.ConditionFalse,
					Reason:  "InterfaceNotMigratable",
					Message: "migration is disabled when VM has a SR-IOV interface",
				}, nil
			}
			if iface.VhostUser != nil {
				return &metav1.Condition{
					Type:    string(v1beta1.VirtualMachineMigratable),
					Status:  metav1.ConditionFalse,
					Reason:  "InterfaceNotMigratable",
					Message: "migration is disable when VM has a vhost-user interface",
				}, nil
			}
		}
	}

	for _, volume := range vm.Spec.Volumes {
		if volume.ContainerRootfs != nil {
			return &metav1.Condition{
				Type:    string(v1beta1.VirtualMachineMigratable),
				Status:  metav1.ConditionFalse,
				Reason:  "VolumeNotMigratable",
				Message: "migration is disabled when VM has a containerRootfs volume",
			}, nil
		}
		if volume.ContainerDisk != nil {
			return &metav1.Condition{
				Type:    string(v1beta1.VirtualMachineMigratable),
				Status:  metav1.ConditionFalse,
				Reason:  "VolumeNotMigratable",
				Message: "migration is disabled when VM has a containerDisk volume",
			}, nil
		}
	}

	if len(vm.Spec.Instance.FileSystems) > 0 {
		return &metav1.Condition{
			Type:    string(v1beta1.VirtualMachineMigratable),
			Status:  metav1.ConditionFalse,
			Reason:  "FileSystemNotMigratable",
			Message: "migration is disabled when VM has a fileSystem",
		}, nil
	}

	return &metav1.Condition{
		Type:   string(v1beta1.VirtualMachineMigratable),
		Status: metav1.ConditionTrue,
		Reason: "Migratable",
	}, nil
}
func (r *VirtualMachineReconciler) buildTargetVMPod(ctx context.Context, vm *v1beta1.VirtualMachine) (*corev1.Pod, error) {
	pod, err := r.buildVMPod(ctx, vm)
	if err != nil {
		return nil, err
	}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "RECEIVE_MIGRATION",
		Value: "true",
	})

	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	affinity := pod.Spec.Affinity

	if affinity.PodAntiAffinity == nil {
		affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
	}
	podAntiAffinity := affinity.PodAntiAffinity
	if podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{}
	}
	podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"quill.today/vm.name": vm.Name,
			},
		},
		TopologyKey: "kubernetes.io/hostname",
	})
	return pod, nil
}

func (r *VirtualMachineReconciler) getHotplugVolumePods(ctx context.Context, vmPod *corev1.Pod) ([]*corev1.Pod, error) {
	podList := corev1.PodList{}
	if err := r.Client.List(ctx, &podList, client.InNamespace(vmPod.Namespace)); err != nil {
		return nil, err
	}

	pods := []*corev1.Pod{}
	for i := range podList.Items {
		ownerRef := metav1.GetControllerOf(&podList.Items[i])
		if ownerRef != nil && ownerRef.UID == vmPod.UID {
			pods = append(pods, &podList.Items[i])
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		return !pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp)
	})
	return pods, nil
}

func (r *VirtualMachineReconciler) updateHotplugVolumeStatus(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod) error {
	//get volume hot plug for the pod
	hotplugVolumes := getHotplugVolumes(vm, vmPod)

	//get the volume status from vm status
	volumeStatusMap := map[string]v1beta1.VolumeStatus{}
	for _, status := range vm.Status.VolumeStatus {
		volumeStatusMap[status.Name] = status
	}

	hotplugVolumePods, err := r.getHotplugVolumePods(ctx, vmPod)
	if err != nil {
		return err
	}

	newVolumeStatus := []v1beta1.VolumeStatus{}

	for _, volume := range hotplugVolumes {
		status := v1beta1.VolumeStatus{}
		if _, ok := volumeStatusMap[volume.Name]; ok {
			status = volumeStatusMap[volume.Name]
		} else {
			status.Name = volume.Name
		}
		if status.Phase == "" {
			status.Phase = v1beta1.VolumePending
		}

		delete(volumeStatusMap, volume.Name)
		if status.HotplugVolume == nil {
			status.HotplugVolume = &v1beta1.HotplugVolumeStatus{}
		}
		var volumePod *corev1.Pod
		for _, pod := range hotplugVolumePods {
			for _, podVolume := range pod.Spec.Volumes {
				if podVolume.Name == volume.Name {
					volumePod = pod
					break
				}
			}
			if volumePod != nil {
				break
			}
		}
		if volumePod != nil {
			status.HotplugVolume.VolumePodName = volumePod.Name
			status.HotplugVolume.VolumePodUID = volumePod.UID
			if volumePod.Status.Phase == corev1.PodRunning && status.Phase == v1beta1.VolumePending {
				status.Phase = v1beta1.VolumeAttachedToNode
			}
		} else {
			status.HotplugVolume.VolumePodName = ""
			status.HotplugVolume.VolumePodUID = ""
			status.Phase = v1beta1.VolumePending
		}
		newVolumeStatus = append(newVolumeStatus, status)
	}

	for volumeName, status := range volumeStatusMap {
		var volumePod *corev1.Pod
		for _, pod := range hotplugVolumePods {
			for _, podVolume := range pod.Spec.Volumes {
				if podVolume.Name == volumeName {
					volumePod = pod
					break
				}
				if volumePod != nil {
					break
				}
			}
		}
		if volumePod != nil {
			status.HotplugVolume.VolumePodName = volumePod.Name
			status.HotplugVolume.VolumePodUID = volumePod.UID
			status.Phase = v1beta1.VolumeDetaching
			newVolumeStatus = append(newVolumeStatus, status)
		}
	}
	vm.Status.VolumeStatus = newVolumeStatus

	for _, status := range vm.Status.VolumeStatus {
		if status.Phase == v1beta1.VolumePending {
			return reconcileError{ctrl.Result{RequeueAfter: time.Minute}}
		}
	}
	return nil
}

func (r *VirtualMachineReconciler) handleHotplugVolumes(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod, waitAllVolumesReady bool) error {
	hotplugVolumes := getHotplugVolumes(vm, vmPod)
	readyHotplugVolumes := []*v1beta1.Volume{}
	for _, volume := range hotplugVolumes {
		ready, err := volumeutil.IsReady(ctx, r.Client, vm.Namespace, *volume)
		if err != nil {
			return err
		}
		if ready {
			readyHotplugVolumes = append(readyHotplugVolumes, volume)
		}
	}
	if waitAllVolumesReady && len(readyHotplugVolumes) < len(hotplugVolumes) {
		return nil
	}

	hotplugVolumePods, err := r.getHotplugVolumePods(ctx, vmPod)
	if err != nil {
		return err
	}

	oldPods := []*corev1.Pod{}
	var currentPod *corev1.Pod
	for _, pod := range hotplugVolumePods {
		if currentPod == nil && isPodMatchVMHotplugVolumes(pod, readyHotplugVolumes) {
			currentPod = pod
		} else {
			oldPods = append(oldPods, pod)
		}
	}

	if currentPod == nil && len(readyHotplugVolumes) > 0 {
		volumePod, err := r.buildHotplugVolumePod(ctx, vm, vmPod, readyHotplugVolumes)
		if err != nil {
			return err
		}
		if err := r.Create(ctx, volumePod); err != nil {
			return err
		}
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "CreatedHotplugVolumePod", fmt.Sprintf("Created VM Hotplug Volume Pod %q", volumePod.Name))
	}

	for _, pod := range oldPods {
		if pod.DeletionTimestamp.IsZero() {
			if err := r.Client.Delete(ctx, pod); err != nil {
				return err
			}
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "DeletedHotplugVolumePod", fmt.Sprintf("Deleted VM Hotplug Volume Pod %q", pod.Name))
		}
	}
	return nil
}

func (r *VirtualMachineReconciler) buildHotplugVolumePod(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod, hotplugVolumes []*v1beta1.Volume) (*corev1.Pod, error) {
	sharedMount := corev1.MountPropagationHostToContainer

	prerunner := r.PrerunnerImageName

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    vmPod.Namespace,
			GenerateName: fmt.Sprintf("%s-hotplug-volumes-", vm.Name),
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			HostNetwork:   true,
			Tolerations:   vmPod.Spec.Tolerations,
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{vmPod.Spec.NodeName},
							}},
						}},
					},
				},
			},
			Containers: []corev1.Container{{
				Name:    "hotplug-volumes",
				Image:   prerunner,
				Command: []string{"/bin/sh", "-c", "ncat -lkU /hotplug/hp.sock"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:             "hotplug",
					MountPath:        "/hotplug",
					MountPropagation: &sharedMount,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "hotplug",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
		},
	}

	volumeStatusMap := map[string]v1beta1.VolumeStatus{}
	for _, volumeStatus := range vm.Status.VolumeStatus {
		volumeStatusMap[volumeStatus.Name] = volumeStatus
	}

	for _, volume := range hotplugVolumes {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volume.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: volume.PVCName(),
				},
			},
		})

		volumeStatus := volumeStatusMap[volume.Name]
		if volumeStatus.Phase != v1beta1.VolumeMountedToPod && volumeStatus.Phase != v1beta1.VolumeReady {
			isBlock, err := volumeutil.IsBlock(ctx, r.Client, vm.Namespace, *volume)
			if err != nil {
				return nil, err
			}
			if isBlock {
				volumeDevice := corev1.VolumeDevice{
					Name:       volume.Name,
					DevicePath: "/hotplug/" + volume.Name,
				}
				pod.Spec.Containers[0].VolumeDevices = append(pod.Spec.Containers[0].VolumeDevices, volumeDevice)
			} else {
				volumeMount := corev1.VolumeMount{
					Name:      volume.Name,
					MountPath: "/mnt/" + volume.Name,
				}
				pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, volumeMount)
			}
		}
	}
	if err := controllerutil.SetControllerReference(vmPod, pod, r.Scheme); err != nil {
		return nil, err
	}
	return pod, nil
}

func isPodMatchVMHotplugVolumes(pod *corev1.Pod, hotplugVolumes []*v1beta1.Volume) bool {
	if len(pod.Spec.Volumes)-2 != len(hotplugVolumes) {
		return false
	}

	podVolumesMap := make(map[string]corev1.Volume)
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			podVolumesMap[volume.Name] = volume
		}
	}
	for _, volume := range hotplugVolumes {
		delete(podVolumesMap, volume.Name)
	}
	return len(podVolumesMap) == 0
}

func getHotplugVolumes(vm *v1beta1.VirtualMachine, vmPod *corev1.Pod) []*v1beta1.Volume {
	podVolumes := make(map[string]bool)
	for _, volume := range vmPod.Spec.Volumes {
		podVolumes[volume.Name] = true
	}

	hotplugVolumes := make([]*v1beta1.Volume, 0)
	for i := range vm.Spec.Volumes {
		volume := vm.Spec.Volumes[i]
		if (volume).IsHotpluggable() && !podVolumes[volume.Name] {
			hotplugVolumes = append(hotplugVolumes, volume)
		}
	}
	return hotplugVolumes
}

func (r *VirtualMachineReconciler) buildVMPod(ctx context.Context, vm *v1beta1.VirtualMachine) (*corev1.Pod, error) {

	prerunner := r.PrerunnerImageName
	vmPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      vm.Labels,
			Annotations: vm.Annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:    corev1.RestartPolicyNever,
			NodeSelector:     vm.Spec.NodeSelector,
			Tolerations:      vm.Spec.Tolerations,
			Affinity:         vm.Spec.Affinity,
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: os.Getenv("REGISTRY_CREDS_SECRET")}},
			Containers: []corev1.Container{{
				Name:           "vm-manager",
				Image:          prerunner,
				Resources:      vm.Spec.Resources,
				LivenessProbe:  vm.Spec.LivenessProbe,
				ReadinessProbe: vm.Spec.ReadinessProbes,
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						Add: []corev1.Capability{"SYS_ADMIN", "NET_ADMIN", "SYS_RESOURCE"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "ch-vmm",
					MountPath: "/var/run/ch-vmm",
				}, {
					Name:             "hotplug-volumes",
					MountPath:        "/hotplug-volumes",
					MountPropagation: &[]corev1.MountPropagationMode{corev1.MountPropagationHostToContainer}[0],
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "ch-vmm",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}, {
				Name: "hotplug-volumes",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
		},
	}

	incrementContainerResource(&vmPod.Spec.Containers[0], "devices.quill.today/kvm")
	incrementContainerResource(&vmPod.Spec.Containers[0], "devices.quill.today/tun")
	if vmPod.Labels == nil {
		vmPod.Labels = map[string]string{}
	}
	vmPod.Labels["quill.today/vm.name"] = vm.Name

	if vm.Spec.Instance.Kernel != nil {
		vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
			Name: "virtmanager-kernel",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})

		volumeMount := corev1.VolumeMount{
			Name:      "virtmanager-kernel",
			MountPath: "/mnt/virtmanager-kernel",
		}
		vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

		vmPod.Spec.InitContainers = append(vmPod.Spec.InitContainers, corev1.Container{
			Name:            "init-kernel",
			Image:           vm.Spec.Instance.Kernel.Image,
			ImagePullPolicy: vm.Spec.Instance.Kernel.ImagePullPolicy,
			Resources:       vm.Spec.Resources,
			Args:            []string{volumeMount.MountPath + "/vmlinux"},
			VolumeMounts:    []corev1.VolumeMount{volumeMount},
		})
	}

	if vm.Spec.Instance.Memory.Hugepages != nil {
		vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
			Name: "hugepages",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: "HugePages",
				},
			},
		})
		volumeMount := corev1.VolumeMount{
			Name:      "hugepages",
			MountPath: "/dev/hugepages",
		}
		vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)
	}

	blockVolumes := []string{}
	for _, volume := range vm.Spec.Volumes {
		switch {
		case volume.ContainerDisk != nil:
			vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})

			volumeMount := corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: "/mnt/" + volume.Name,
			}
			vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

			vmPod.Spec.InitContainers = append(vmPod.Spec.InitContainers, corev1.Container{
				Name:            "init-volume-" + volume.Name,
				Image:           volume.ContainerDisk.Image,
				ImagePullPolicy: volume.ContainerDisk.ImagePullPolicy,
				Resources:       vm.Spec.Resources,
				Args:            []string{volumeMount.MountPath + "/disk.raw"},
				VolumeMounts:    []corev1.VolumeMount{volumeMount},
			})
		case volume.CloudInit != nil:
			initContainer := corev1.Container{
				Name:      "init-volume-" + volume.Name,
				Image:     vmPod.Spec.Containers[0].Image,
				Resources: vm.Spec.Resources,
				Command:   []string{"virt-init-volume"},
				Args:      []string{"cloud-init"},
			}

			metaData := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("instance-id: %s\nlocal-hostname: %s", vm.UID, vm.Name)))
			initContainer.Args = append(initContainer.Args, metaData)

			var userData string
			switch {
			case volume.CloudInit.UserData != "":
				userData = base64.StdEncoding.EncodeToString([]byte(volume.CloudInit.UserData))
			case volume.CloudInit.UserDataBase64 != "":
				userData = volume.CloudInit.UserDataBase64
			case volume.CloudInit.UserDataSecretName != "":
				vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
					Name: "virtmanager-cloud-init-user-data",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: volume.CloudInit.UserDataSecretName,
						},
					},
				})
				initContainer.VolumeMounts = append(initContainer.VolumeMounts, corev1.VolumeMount{
					Name:      "virtmanager-cloud-init-user-data",
					MountPath: "/mnt/virtmanager-cloud-init-user-data",
				})
				userData = "/mnt/virtmanager-cloud-init-user-data/value"
			default:
				// ignored
			}
			initContainer.Args = append(initContainer.Args, userData)

			var networkData string
			switch {
			case volume.CloudInit.NetworkData != "":
				networkData = base64.StdEncoding.EncodeToString([]byte(volume.CloudInit.NetworkData))
			case volume.CloudInit.NetworkDataBase64 != "":
				networkData = volume.CloudInit.NetworkDataBase64
			case volume.CloudInit.NetworkDataSecretName != "":
				vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
					Name: "virtmanager-cloud-init-network-data",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: volume.CloudInit.NetworkDataSecretName,
						},
					},
				})
				vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
					Name:      "virtmanager-cloud-init-network-data",
					MountPath: "/mnt/virtmanager-cloud-init-network-data",
				})
				networkData = "/mnt/virtmanager-cloud-init-network-data/value"
			default:
				// ignored
			}
			initContainer.Args = append(initContainer.Args, networkData)

			vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})

			volumeMount := corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: "/mnt/" + volume.Name,
			}
			vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)
			initContainer.VolumeMounts = append(initContainer.VolumeMounts, volumeMount)
			initContainer.Args = append(initContainer.Args, volumeMount.MountPath+"/cloud-init.iso")
			vmPod.Spec.InitContainers = append(vmPod.Spec.InitContainers, initContainer)
		case volume.ContainerRootfs != nil:
			vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})

			volumeMount := corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: "/mnt/" + volume.Name,
			}
			vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

			vmPod.Spec.InitContainers = append(vmPod.Spec.InitContainers, corev1.Container{
				Name:            "init-volume-" + volume.Name,
				Image:           volume.ContainerRootfs.Image,
				ImagePullPolicy: volume.ContainerRootfs.ImagePullPolicy,
				Resources:       vm.Spec.Resources,
				Args:            []string{volumeMount.MountPath + "/rootfs.raw", strconv.FormatInt(volume.ContainerRootfs.Size.Value(), 10)},
				VolumeMounts:    []corev1.VolumeMount{volumeMount},
			})
		case volume.MemorySnapshot != nil:
			vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})

			volumeMount := corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: "/mnt/" + volume.Name,
			}
			vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

		case volume.PersistentVolumeClaim != nil, volume.DataVolume != nil, volume.VirtualDisk != nil:

			ready, err := volumeutil.IsReady(ctx, r.Client, vm.Namespace, *volume)
			if err != nil {
				ctrl.Log.Error(err, "Volumeutil ready error", "volume", volume.Name, "vm", vm.Name)
				return nil, err
			}
			if !ready {
				ctrl.Log.Error(err, "Volumeutil volume ready check", "volume", volume.Name, "vm", vm.Name)
				return nil, reconcileError{Result: ctrl.Result{RequeueAfter: time.Minute}}
			}

			isBlock, err := volumeutil.IsBlock(ctx, r.Client, vm.Namespace, *volume)
			if err != nil {
				ctrl.Log.Error(err, "Volumeutil block check", "volume", volume.Name, "vm", vm.Name)
				return nil, err
			}

			if isBlock {
				blockVolumes = append(blockVolumes, volume.Name)
			}

			if !volume.IsHotpluggable() {
				vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
					Name: volume.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: volume.PVCName(),
						},
					},
				})
				if isBlock {
					volumeDevice := corev1.VolumeDevice{
						Name:       volume.Name,
						DevicePath: "/mnt/" + volume.Name,
					}
					vmPod.Spec.Containers[0].VolumeDevices = append(vmPod.Spec.Containers[0].VolumeDevices, volumeDevice)
				} else {
					volumeMount := corev1.VolumeMount{
						Name:      volume.Name,
						MountPath: "/mnt/" + volume.Name,
					}
					vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)
				}
			}
		default:
			// ignored
		}
	}
	if len(blockVolumes) > 0 {
		vmPod.Spec.Containers[0].Env = append(vmPod.Spec.Containers[0].Env, corev1.EnvVar{Name: "BLOCK_VOLUMES", Value: strings.Join(blockVolumes, ",")})
	}

	for _, gpu := range vm.Spec.Instance.GPUs {
		incrementContainerResource(&vmPod.Spec.Containers[0], gpu.ResourceName)
	}

	var networks []netv1.NetworkSelectionElement
	numOfUserspaceIface := 0
	for i, network := range vm.Spec.Networks {
		var iface *v1beta1.Interface
		for j := range vm.Spec.Instance.Interfaces {
			if vm.Spec.Instance.Interfaces[j].Name == network.Name {
				iface = &vm.Spec.Instance.Interfaces[j]
				break
			}
		}
		if iface == nil {
			ctrl.Log.Error(errors.New("Interface Not found"), "Invalid Data", "vm", vm.Name)
			return nil, fmt.Errorf("interface not found for network: %s", network.Name)
		}

		if iface.Masquerade != nil {
			var prerunner string
			prerunner = r.PrerunnerImageName

			vmPod.Spec.InitContainers = append(vmPod.Spec.InitContainers, corev1.Container{
				Name:      "enable-ip-forward",
				Image:     prerunner,
				Resources: vm.Spec.Resources,
				SecurityContext: &corev1.SecurityContext{
					Privileged: &[]bool{true}[0],
				},
				Command: []string{"sysctl", "-w", "net.ipv4.ip_forward=1"},
			})
		}

		switch {
		case network.Multus != nil:
			networks = append(networks, netv1.NetworkSelectionElement{
				Name:             network.Multus.NetworkName,
				InterfaceRequest: fmt.Sprintf("net%d", i),
				MacRequest:       iface.MAC,
			})

			var nad netv1.NetworkAttachmentDefinition
			nadKey := types.NamespacedName{
				Name:      network.Multus.NetworkName,
				Namespace: vm.Namespace,
			}
			if err := r.Client.Get(ctx, nadKey, &nad); err != nil {
				ctrl.Log.Error(err, " client nadKey error", "nadKey", nadKey, "vm", vm.Name)
				return nil, fmt.Errorf("get NAD: %s", err)
			}

			resourceName := nad.Annotations["k8s.v1.cni.cncf.io/resourceName"]
			if resourceName != "" {
				incrementContainerResource(&vmPod.Spec.Containers[0], resourceName)
			}
			vmPod.Spec.Containers[0].Env = append(vmPod.Spec.Containers[0].Env, corev1.EnvVar{
				Name: "NETWORK_STATUS",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", netv1.NetworkStatusAnnot),
					},
				},
			})

			if iface.VhostUser != nil {
				type nadConfig struct {
					Type                      string `json:"type"`
					VhostUserSocketVolumeName string `json:"vhost_user_socket_volume_name,omitempty"`
					VhostUserSocketName       string `json:"vhost_user_socket_name,omitempty"`
				}

				var cfg nadConfig
				if err := json.Unmarshal([]byte(nad.Spec.Config), &cfg); err != nil {
					ctrl.Log.Error(err, "Networks CNI Plugin not supported ", "vm", vm.Name)
					return nil, fmt.Errorf("unmarshal NAD config: %s", err)
				}

				switch cfg.Type {
				case "kube-ovn":
					if vmPod.Spec.NodeSelector == nil {
						vmPod.Spec.NodeSelector = map[string]string{}
					}
					vmPod.Spec.NodeSelector["ovn.kubernetes.io/ovs_dp_type"] = "userspace"
					vmPod.Annotations["ovn-dpdk.default.ovn.kubernetes.io/mac_address"] = iface.MAC

					vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
						Name: cfg.VhostUserSocketVolumeName,
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					})
					volumeMount := corev1.VolumeMount{
						Name:      cfg.VhostUserSocketVolumeName,
						MountPath: "/var/run/vhost-user",
					}
					vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

					vmPod.Spec.Containers[0].Env = append(vmPod.Spec.Containers[0].Env,
						corev1.EnvVar{
							Name:  "VHOST_USER_SOCKET",
							Value: fmt.Sprintf("/var/run/vhost-user/%s", cfg.VhostUserSocketName),
						},
						corev1.EnvVar{
							Name:  "NET_TYPE",
							Value: "kube-ovn",
						},
					)
				case "userspace":
					numOfUserspaceIface++
					if numOfUserspaceIface == 1 {
						vmPod.Spec.Volumes = append(vmPod.Spec.Volumes, corev1.Volume{
							Name: "vhost-user-sockets",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/usr/local/var/run/openvswitch/",
								},
							},
						})
						volumeMount := corev1.VolumeMount{
							Name:      "vhost-user-sockets",
							MountPath: "/var/run/vhost-user",
						}
						vmPod.Spec.Containers[0].VolumeMounts = append(vmPod.Spec.Containers[0].VolumeMounts, volumeMount)

						vmPod.Spec.Containers[0].Env = append(vmPod.Spec.Containers[0].Env,
							corev1.EnvVar{
								Name: "USERSPACE_CONFIGURATION_DATA",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.annotations['userspace/configuration-data']",
									},
								},
							},
							corev1.EnvVar{
								Name:  "NET_TYPE",
								Value: "userspace",
							},
						)
					}
				default:
					ctrl.Log.Info("Networks CNI Plugin not supported ", "nad", cfg.Type, "vm", vm.Name)
					return nil, fmt.Errorf("CNI plugin %s is not supported for vhost-uesr", cfg.Type)
				}
			}
		default:
			// ignored
		}
	}

	if len(networks) > 0 {
		networksJSON, err := json.Marshal(networks)
		if err != nil {
			ctrl.Log.Error(err, "Networks Json marshalling error", "networks", networksJSON, "vm", vm.Name)
			return nil, fmt.Errorf("marshal networks: %s", err)
		}
		vmPod.Annotations["k8s.v1.cni.cncf.io/networks"] = string(networksJSON)
	}

	vmJSON, err := json.Marshal(vm)
	if err != nil {
		ctrl.Log.Error(err, "vm Json marshalling error", "networks", vmJSON, "vm", vm.Name)
		return nil, fmt.Errorf("marshal VM: %s", err)
	}
	vmPod.Spec.Containers[0].Env = append(vmPod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "VM_DATA",
		Value: base64.StdEncoding.EncodeToString(vmJSON),
	})

	return &vmPod, nil
}

func (r *VirtualMachineReconciler) deleteAllVMPods(ctx context.Context, vm *v1beta1.VirtualMachine) (bool, error) {
	var vmPodList corev1.PodList
	if err := r.List(ctx, &vmPodList, client.MatchingFields{"vmUID": string(vm.UID)}); err != nil {
		return false, fmt.Errorf("list VM Pods: %s", err)
	}

	if len(vmPodList.Items) == 0 {
		return true, nil
	}

	for _, vmPod := range vmPodList.Items {
		if vmPod.DeletionTimestamp != nil && !vmPod.DeletionTimestamp.IsZero() {
			continue
		}

		if err := r.Delete(ctx, &vmPod); client.IgnoreNotFound(err) != nil {
			return false, fmt.Errorf("delete VM Pod: %s", err)
		}
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "DeletedVMPod", fmt.Sprintf("Deleted VM Pod %q", vmPod.Name))
	}

	return true, nil

}
func incrementContainerResource(container *corev1.Container, resourceName string) {
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}
	request := container.Resources.Requests[corev1.ResourceName(resourceName)]
	request = resource.MustParse(strconv.FormatInt(request.Value()+1, 10))
	container.Resources.Requests[corev1.ResourceName(resourceName)] = request

	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{}
	}
	limit := container.Resources.Limits[corev1.ResourceName(resourceName)]
	limit = resource.MustParse(strconv.FormatInt(limit.Value()+1, 10))
	container.Resources.Limits[corev1.ResourceName(resourceName)] = limit
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log.Log.Info("Setting up VM controller")
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "vmUID", func(obj client.Object) []string {
		pod := obj.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner != nil && owner.APIVersion == v1beta1.GroupVersion.String() && owner.Kind == "VirtualMachine" {
			return []string{string(owner.UID)}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("index Pods by VM UID: %s", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachine{}).
		Owns(&corev1.Pod{}).
		Named("virtualmachine").
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			if _, ok := obj.(*corev1.Pod); !ok {
				return nil
			}
			controllerRef := metav1.GetControllerOf(obj)
			if controllerRef != nil && controllerRef.Kind == "Pod" {
				pod := corev1.Pod{}
				podKey := types.NamespacedName{Namespace: obj.GetNamespace(), Name: controllerRef.Name}
				if err := r.Client.Get(context.Background(), podKey, &pod); err != nil {
					return nil
				}
				controllerRef = metav1.GetControllerOf(&pod)
			}

			if controllerRef == nil || controllerRef.Kind != "VirtualMachine" {
				return nil
			}

			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: obj.GetNamespace(),
					Name:      controllerRef.Name,
				},
			}}
		})).
		Watches(&corev1.PersistentVolumeClaim{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); !ok {
				return nil
			}
			var vmList v1beta1.VirtualMachineList
			if err := r.Client.List(context.Background(), &vmList, client.InNamespace(obj.GetNamespace())); err != nil {
				return nil
			}
			requests := []reconcile.Request{}
			for _, vm := range vmList.Items {
				for _, volume := range vm.Spec.Volumes {
					if volume.PVCName() == obj.GetName() {
						requests = append(requests, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Namespace: vm.Namespace,
								Name:      vm.Name,
							},
						})
						break
					}
				}
			}
			return requests
		})).
		Complete(r)
}
