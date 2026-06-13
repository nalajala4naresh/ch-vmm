package webhooks

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"reflect"
	"strings"

	types "github.com/nalajala4naresh/chvmm-api/v1beta1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var memoryOverhead = "256Mi"

// log is for logging in this package.
var virtualmachinelog = logf.Log.WithName("virtualmachine-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func SetupVirtualMachineWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&types.VirtualMachine{}).
		WithDefaulter(&VirtualMachineCustomDefaulter{}).
		WithValidator(&VirtualMachineCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-cloudhypervisor-quill-today-v1beta1-virtualmachine,mutating=true,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=create;update,versions=v1beta1,name=mvirtualmachine.kb.io,admissionReviewVersions=v1

// VirtualMachineCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualMachine when those are created or updated.
type VirtualMachineCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VirtualMachineCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *VirtualMachineCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {

	// TODO(user): fill in your defaulting logic.
	vm := obj.(*types.VirtualMachine)
	if vm.Namespace == "" {
		vm.Namespace = "default"
	}

	return MutateVM(ctx, vm, nil)
}

func MutateVM(ctx context.Context, vm *types.VirtualMachine, oldVM *types.VirtualMachine) error {

	if vm.Spec.RunPolicy == "" {
		vm.Spec.RunPolicy = types.RunPolicyOnce
	}

	if vm.Spec.Instance.CPU.Sockets == 0 {
		vm.Spec.Instance.CPU.Sockets = 1
	}
	if vm.Spec.Instance.CPU.CoresPerSocket == 0 {
		vm.Spec.Instance.CPU.CoresPerSocket = 1
	}

	if vm.Spec.Instance.Memory.Size.IsZero() {
		if !vm.Spec.Resources.Requests.Memory().IsZero() {
			vm.Spec.Instance.Memory.Size = vm.Spec.Resources.Requests.Memory().DeepCopy()
		} else {
			vm.Spec.Instance.Memory.Size = resource.MustParse("1Gi")
		}
	}

	if vm.Spec.Instance.CPU.DedicatedCPUPlacement {
		memSize := resource.MustParse(memoryOverhead)
		if !vm.Spec.Instance.Memory.Size.IsZero() {
			if vm.Spec.Instance.Memory.Hugepages == nil {
				memSize.Add(vm.Spec.Instance.Memory.Size)
			}
		}
		rsList := map[corev1.ResourceName]resource.Quantity{
			corev1.ResourceCPU:    *resource.NewQuantity(int64(vm.Spec.Instance.CPU.CoresPerSocket*vm.Spec.Instance.CPU.Sockets), resource.DecimalSI),
			corev1.ResourceMemory: memSize,
		}

		if vm.Spec.Resources.Requests == nil {
			vm.Spec.Resources.Requests = rsList
		} else {
			if vm.Spec.Resources.Requests.Cpu().IsZero() {
				vm.Spec.Resources.Requests[corev1.ResourceCPU] = rsList[corev1.ResourceCPU]
			}
			if vm.Spec.Resources.Requests.Memory().IsZero() {
				vm.Spec.Resources.Requests[corev1.ResourceMemory] = rsList[corev1.ResourceMemory]
			}
		}

		if vm.Spec.Resources.Limits == nil {
			vm.Spec.Resources.Limits = rsList
		} else {
			if vm.Spec.Resources.Limits.Cpu().IsZero() {
				vm.Spec.Resources.Limits[corev1.ResourceCPU] = rsList[corev1.ResourceCPU]
			}
			if vm.Spec.Resources.Limits.Memory().IsZero() {
				vm.Spec.Resources.Limits[corev1.ResourceMemory] = rsList[corev1.ResourceMemory]
			}
		}
	}

	if vm.Spec.Instance.Memory.Hugepages != nil {
		hugepagesSize := fmt.Sprintf("hugepages-%s", vm.Spec.Instance.Memory.Hugepages.PageSize)

		if vm.Spec.Resources.Limits == nil {
			vm.Spec.Resources.Limits = corev1.ResourceList{}
		}
		hugepagesLimit, exist := vm.Spec.Resources.Limits[corev1.ResourceName(hugepagesSize)]
		if !exist {
			hugepagesLimit = vm.Spec.Instance.Memory.Size.DeepCopy()
			vm.Spec.Resources.Limits[corev1.ResourceName(hugepagesSize)] = hugepagesLimit
		}
		if vm.Spec.Resources.Requests == nil {
			vm.Spec.Resources.Requests = corev1.ResourceList{}
		}
		if _, exist := vm.Spec.Resources.Requests[corev1.ResourceName(hugepagesSize)]; !exist {
			vm.Spec.Resources.Requests[corev1.ResourceName(hugepagesSize)] = hugepagesLimit.DeepCopy()
		}

		if vm.Spec.Resources.Limits.Cpu().IsZero() && vm.Spec.Resources.Limits.Memory().IsZero() && vm.Spec.Resources.Requests.Cpu().IsZero() && vm.Spec.Resources.Requests.Memory().IsZero() {
			vm.Spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memoryOverhead)
		}
	}

	// Default CPU and memory resources if not set (for general case, not just DedicatedCPUPlacement)
	// This ensures Pods always have resource limits for vertical scaling
	if !vm.Spec.Instance.CPU.DedicatedCPUPlacement {
		if vm.Spec.Resources.Requests == nil {
			vm.Spec.Resources.Requests = corev1.ResourceList{}
		}
		if vm.Spec.Resources.Limits == nil {
			vm.Spec.Resources.Limits = corev1.ResourceList{}
		}

		// Calculate desired resources from VM instance spec
		desiredCPU := resource.NewQuantity(int64(vm.Spec.Instance.CPU.Sockets*vm.Spec.Instance.CPU.CoresPerSocket), resource.DecimalSI)
		memOverhead := resource.MustParse(memoryOverhead)
		desiredMem := vm.Spec.Instance.Memory.Size.DeepCopy()
		desiredMem.Add(memOverhead)

		// Set CPU request if missing
		if vm.Spec.Resources.Requests.Cpu().IsZero() {
			vm.Spec.Resources.Requests[corev1.ResourceCPU] = *desiredCPU
		}
		// Set CPU limit if missing
		if vm.Spec.Resources.Limits.Cpu().IsZero() {
			vm.Spec.Resources.Limits[corev1.ResourceCPU] = *desiredCPU
		}

		// Set memory request if missing
		if vm.Spec.Resources.Requests.Memory().IsZero() {
			vm.Spec.Resources.Requests[corev1.ResourceMemory] = desiredMem
		}
		// Set memory limit if missing
		if vm.Spec.Resources.Limits.Memory().IsZero() {
			vm.Spec.Resources.Limits[corev1.ResourceMemory] = desiredMem
		}
	}

	// Adding GPU Default ResourceEnvName
	for i := range vm.Spec.Instance.GPUs {
		if vm.Spec.Instance.GPUs[i].ResourceEnvName == "" {
			if strings.Contains(vm.Spec.Instance.GPUs[i].ResourceName, "nvidia.com/") {
				deviceName := strings.TrimPrefix(vm.Spec.Instance.GPUs[i].ResourceName, "nvidia.com/")
				vm.Spec.Instance.GPUs[i].ResourceEnvName = fmt.Sprintf("PCI_RESOURCE_NVIDIA_COM_%s", deviceName)

			}

		}
	}

	for i := range vm.Spec.Instance.Interfaces {
		if vm.Spec.Instance.Interfaces[i].MAC == "" {
			var macStr string
			if oldVM != nil {
				for j := range oldVM.Spec.Instance.Interfaces {
					if oldVM.Spec.Instance.Interfaces[j].Name == vm.Spec.Instance.Interfaces[i].Name {
						macStr = oldVM.Spec.Instance.Interfaces[j].MAC
						break
					}
				}
			}
			if macStr == "" {
				mac, err := generateMAC()
				if err != nil {
					return fmt.Errorf("generate MAC: %s", err)
				}
				macStr = mac.String()
			}
			vm.Spec.Instance.Interfaces[i].MAC = macStr
		}

		if vm.Spec.Instance.Interfaces[i].Bridge == nil && vm.Spec.Instance.Interfaces[i].Masquerade == nil && vm.Spec.Instance.Interfaces[i].SRIOV == nil && vm.Spec.Instance.Interfaces[i].VDPA == nil && vm.Spec.Instance.Interfaces[i].VhostUser == nil {
			vm.Spec.Instance.Interfaces[i].InterfaceBindingMethod = types.InterfaceBindingMethod{
				Bridge: &types.InterfaceBridge{},
			}
		}

		if vm.Spec.Instance.Interfaces[i].Masquerade != nil {
			if vm.Spec.Instance.Interfaces[i].Masquerade.CIDR == "" {
				vm.Spec.Instance.Interfaces[i].Masquerade.CIDR = "10.0.2.0/30"
			}
		}
	}
	return nil
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-cloudhypervisor-quill-today-v1beta1-virtualmachine,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=create;update,versions=v1beta1,name=vvirtualmachine.kb.io,admissionReviewVersions=v1

// VirtualMachineCustomValidator struct is responsible for validating the VirtualMachine resource
// when it is created, updated, or deleted.
type VirtualMachineCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &VirtualMachineCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *VirtualMachineCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {

	vm := obj.(*types.VirtualMachine)
	errList := ValidateVM(ctx, vm, nil)
	if len(errList) != 0 {
		errStr := []string{}
		for _, err := range errList {
			errStr = append(errStr, err.Detail)
		}
		return admission.Warnings(errStr), nil

	}

	return nil, nil

}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *VirtualMachineCustomValidator) ValidateUpdate(ctx context.Context, old runtime.Object, new runtime.Object) (admission.Warnings, error) {

	oldvm := old.(*types.VirtualMachine)
	newvm := new.(*types.VirtualMachine)
	errList := ValidateVM(ctx, newvm, oldvm)
	if len(errList) != 0 {
		errStr := []string{}
		for _, err := range errList {
			errStr = append(errStr, err.Detail)
		}
		return admission.Warnings(errStr), nil
	}

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *VirtualMachineCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	vm := obj.(*types.VirtualMachine)
	virtualmachinelog.Info("validate delete", "name", vm.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

func ValidateVM(ctx context.Context, vm *types.VirtualMachine, oldVM *types.VirtualMachine) field.ErrorList {
	errList := field.ErrorList{}

	errList = append(errList, ValidateVMSpec(ctx, &vm.Spec, field.NewPath("spec"))...)

	if oldVM != nil {
		errList = append(errList, ValidateVMUpdate(ctx, vm, oldVM)...)
	}

	return errList
}

func ValidateVMSpec(ctx context.Context, spec *types.VirtualMachineSpec, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if spec.Instance.CPU.Sockets == 0 {
		errList = append(errList, field.Required(fieldPath.Child("instance").Child("cpu").Child("sockets"), "sockets must be greater than 0"))
	}

	if spec.Instance.CPU.CoresPerSocket == 0 {
		errList = append(errList, field.Required(fieldPath.Child("instance").Child("cpu").Child("coresPerSocket"), "coresPerSocket must be greater than 0"))
	}

	if spec.Instance.Memory.Size.IsZero() {
		errList = append(errList, field.Required(fieldPath.Child("instance").Child("memory").Child("size"), "memory size must be greater than 0"))
	}

	if spec.RunPolicy == "" {
		errList = append(errList, field.Required(fieldPath.Child("runPolicy"), "runPolicy must be set"))
	}

	if spec.Instance.CPU.DedicatedCPUPlacement && spec.Instance.Memory.Hugepages == nil {
		errList = append(errList, field.Required(fieldPath.Child("instance").Child("memory").Child("hugepages"), "hugepages must be set when dedicatedCPUPlacement is true"))
	}

	if spec.Instance.CPU.DedicatedCPUPlacement && spec.Instance.CPU.Sockets*spec.Instance.CPU.CoresPerSocket > 1 {
		cpuTotal := spec.Instance.CPU.Sockets * spec.Instance.CPU.CoresPerSocket
		if spec.Resources.Requests == nil || spec.Resources.Requests.Cpu().IsZero() {
			errList = append(errList, field.Required(fieldPath.Child("resources").Child("requests").Child("cpu"), "cpu requests must be set when dedicatedCPUPlacement is true"))
		} else {
			if spec.Resources.Requests.Cpu().MilliValue() != int64(cpuTotal*1000) {
				errList = append(errList, field.Invalid(fieldPath.Child("resources").Child("requests").Child("cpu"), spec.Resources.Requests.Cpu(), "cpu requests must match the total CPU count"))
			}
		}
	}

	errList = append(errList, ValidateInstance(ctx, &spec.Instance, fieldPath.Child("instance"))...)

	for i := range spec.Volumes {
		errList = append(errList, ValidateVolume(ctx, spec.Volumes[i], fieldPath.Child("volumes").Index(i))...)
	}

	for i := range spec.Networks {
		errList = append(errList, ValidateNetwork(ctx, &spec.Networks[i], fieldPath.Child("networks").Index(i))...)
	}

	return errList
}

func ValidateInstance(ctx context.Context, instance *types.Instance, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	errList = append(errList, ValidateCPU(ctx, &instance.CPU, fieldPath.Child("cpu"))...)
	errList = append(errList, ValidateMemory(ctx, &instance.Memory, fieldPath.Child("memory"))...)

	if instance.Kernel != nil {
		errList = append(errList, ValidateKernel(ctx, instance.Kernel, fieldPath.Child("kernel"))...)
	}

	for i, disk := range instance.Disks {
		errList = append(errList, ValidateDisk(ctx, &disk, fieldPath.Child("disks").Index(i))...)
	}

	for i, fs := range instance.FileSystems {
		errList = append(errList, ValidateFileSystem(ctx, &fs, fieldPath.Child("filesystems").Index(i))...)
	}

	for i, iface := range instance.Interfaces {
		errList = append(errList, ValidateInterface(ctx, &iface, fieldPath.Child("interfaces").Index(i))...)
	}

	for i, gpu := range instance.GPUs {
		errList = append(errList, ValidateGPU(ctx, &gpu, fieldPath.Child("gpus").Index(i))...)
	}

	return errList
}

func ValidateCPU(ctx context.Context, cpu *types.CPU, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if cpu.Sockets == 0 {
		errList = append(errList, field.Required(fieldPath.Child("sockets"), "sockets must be greater than 0"))
	}

	if cpu.CoresPerSocket == 0 {
		errList = append(errList, field.Required(fieldPath.Child("coresPerSocket"), "coresPerSocket must be greater than 0"))
	}

	return errList
}

func ValidateMemory(ctx context.Context, memory *types.Memory, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if memory.Size.IsZero() {
		errList = append(errList, field.Required(fieldPath.Child("size"), "memory size must be greater than 0"))
	}

	if memory.Hugepages != nil {
		if memory.Hugepages.PageSize == "" {
			errList = append(errList, field.Required(fieldPath.Child("hugepages").Child("pageSize"), "hugepages pageSize must be set"))
		} else {
			switch memory.Hugepages.PageSize {
			case "2Mi", "1Gi":
				// valid
			default:
				errList = append(errList, field.Invalid(fieldPath.Child("hugepages").Child("pageSize"), memory.Hugepages.PageSize, "hugepages pageSize must be 2Mi or 1Gi"))
			}
		}
	}

	return errList
}

func ValidateKernel(ctx context.Context, kernel *types.Kernel, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if kernel.Image == "" {
		errList = append(errList, field.Required(fieldPath.Child("image"), "kernel image must be set"))
	}

	return errList
}

func ValidateDisk(ctx context.Context, disk *types.Disk, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if disk.Name == "" {
		errList = append(errList, field.Required(fieldPath.Child("name"), "disk name must be set"))
	}

	return errList
}

func ValidateGPU(ctx context.Context, gpu *types.GPU, fieldPath *field.Path) field.ErrorList {
	var errs field.ErrorList
	if gpu == nil {
		errs = append(errs, field.Required(fieldPath, ""))
		return errs
	}

	if gpu.ResourceEnvName == "" {
		errs = append(errs, field.Required(fieldPath.Child("resourceEnvName"), ""))
	}
	return errs
}

func ValidateFileSystem(ctx context.Context, fs *types.FileSystem, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if fs.Name == "" {
		errList = append(errList, field.Required(fieldPath.Child("name"), "filesystem name must be set"))
	}

	return errList
}

func ValidateInterface(ctx context.Context, iface *types.Interface, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if iface.Name == "" {
		errList = append(errList, field.Required(fieldPath.Child("name"), "interface name must be set"))
	}

	if iface.MAC != "" {
		errList = append(errList, ValidateMAC(iface.MAC, fieldPath.Child("mac"))...)
	}

	errList = append(errList, ValidateInterfaceBindingMethod(ctx, &iface.InterfaceBindingMethod, fieldPath.Child("interfaceBindingMethod"))...)

	return errList
}

func ValidateMAC(mac string, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if _, err := net.ParseMAC(mac); err != nil {
		errList = append(errList, field.Invalid(fieldPath, mac, "invalid MAC address"))
	}

	return errList
}

func ValidateInterfaceBindingMethod(ctx context.Context, bindingMethod *types.InterfaceBindingMethod, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	count := 0
	if bindingMethod.Bridge != nil {
		count++
	}
	if bindingMethod.Masquerade != nil {
		count++
		if bindingMethod.Masquerade.CIDR == "" {
			errList = append(errList, field.Required(fieldPath.Child("masquerade").Child("cidr"), "masquerade CIDR must be set"))
		} else {
			errList = append(errList, ValidateCIDR(bindingMethod.Masquerade.CIDR, 4, fieldPath.Child("masquerade").Child("cidr"))...)
		}
	}
	if bindingMethod.SRIOV != nil {
		count++
	}
	if bindingMethod.VDPA != nil {
		count++
	}
	if bindingMethod.VhostUser != nil {
		count++
	}

	if count == 0 {
		errList = append(errList, field.Required(fieldPath, "at least one interface binding method must be set"))
	}

	if count > 1 {
		errList = append(errList, field.Invalid(fieldPath, bindingMethod, "only one interface binding method can be set"))
	}

	return errList
}

func ValidateCIDR(cidr string, capacity int, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		errList = append(errList, field.Invalid(fieldPath, cidr, "invalid CIDR"))
		return errList
	}

	ones, bits := network.Mask.Size()
	if bits-ones < capacity {
		errList = append(errList, field.Invalid(fieldPath, cidr, fmt.Sprintf("CIDR must have at least %d IP addresses", capacity)))
	}

	return errList
}

func ValidateVolume(ctx context.Context, volume *types.Volume, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if volume.Name == "" {
		errList = append(errList, field.Required(fieldPath.Child("name"), "volume name must be set"))
	}

	errList = append(errList, ValidateVolumeSource(ctx, &volume.VolumeSource, fieldPath.Child("volumeSource"))...)

	return errList
}

func ValidateVolumeSource(ctx context.Context, source *types.VolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	count := 0
	if source.ContainerDisk != nil {
		count++
		errList = append(errList, ValidateContainerDiskVolumeSource(ctx, source.ContainerDisk, fieldPath.Child("containerDisk"))...)
	}
	if source.CloudInit != nil {
		count++
		errList = append(errList, ValidateCloudInitVolumeSource(ctx, source.CloudInit, fieldPath.Child("cloudInit"))...)
	}
	if source.ContainerRootfs != nil {
		count++
		errList = append(errList, ValidateContainerRootfsVolumeSource(ctx, source.ContainerRootfs, fieldPath.Child("containerRootfs"))...)
	}
	if source.PersistentVolumeClaim != nil {
		count++
		errList = append(errList, ValidatePersistentVolumeClaimSource(ctx, source.PersistentVolumeClaim, fieldPath.Child("persistentVolumeClaim"))...)
	}
	if source.DataVolume != nil {
		count++
		errList = append(errList, ValidateDataVolumeSource(ctx, source.DataVolume, fieldPath.Child("dataVolume"))...)
	}
	if source.VirtualDisk != nil {
		count++
		errList = append(errList, ValidateVirtualDiskSource(ctx, source.VirtualDisk, fieldPath.Child("virtualDisk"))...)
	}

	if count == 0 {
		errList = append(errList, field.Required(fieldPath, "at least one volume source must be set"))
	}

	if count > 1 {
		errList = append(errList, field.Invalid(fieldPath, source, "only one volume source can be set"))
	}

	return errList
}

func ValidateContainerDiskVolumeSource(ctx context.Context, source *types.ContainerDiskVolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.Image == "" {
		errList = append(errList, field.Required(fieldPath.Child("image"), "container disk image must be set"))
	}

	return errList
}

func ValidateCloudInitVolumeSource(ctx context.Context, source *types.CloudInitVolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	count := 0
	if source.UserData != "" {
		count++
	}
	if source.UserDataBase64 != "" {
		count++
	}
	if source.UserDataSecretName != "" {
		count++
	}
	if source.NetworkData != "" {
		count++
	}
	if source.NetworkDataBase64 != "" {
		count++
	}
	if source.NetworkDataSecretName != "" {
		count++
	}

	if count == 0 {
		errList = append(errList, field.Required(fieldPath, "at least one cloud-init data source must be set"))
	}

	return errList
}

func ValidateContainerRootfsVolumeSource(ctx context.Context, source *types.ContainerRootfsVolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.Image == "" {
		errList = append(errList, field.Required(fieldPath.Child("image"), "container rootfs image must be set"))
	}

	return errList
}

func ValidatePersistentVolumeClaimSource(ctx context.Context, source *types.PersistentVolumeClaimVolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.ClaimName == "" {
		errList = append(errList, field.Required(fieldPath.Child("claimName"), "persistent volume claim name must be set"))
	}

	return errList
}

func ValidateDataVolumeSource(ctx context.Context, source *types.DataVolumeVolumeSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.VolumeName == "" {
		errList = append(errList, field.Required(fieldPath.Child("volumeName"), "data volume name must be set"))
	}

	return errList
}

func ValidateVirtualDiskSource(ctx context.Context, source *types.VirtualDiskVMSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.VirtualDiskName == "" {
		errList = append(errList, field.Required(fieldPath.Child("virtualDiskName"), "virtual disk name must be set"))
	}

	return errList
}

func ValidateNetwork(ctx context.Context, network *types.Network, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if network.Name == "" {
		errList = append(errList, field.Required(fieldPath.Child("name"), "network name must be set"))
	}

	errList = append(errList, ValidateNetworkSource(ctx, &network.NetworkSource, fieldPath.Child("networkSource"))...)

	return errList
}

func ValidateNetworkSource(ctx context.Context, source *types.NetworkSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	count := 0
	if source.Pod != nil {
		count++
		errList = append(errList, ValidatePodNetworkSource(ctx, source.Pod, fieldPath.Child("pod"))...)
	}
	if source.Multus != nil {
		count++
		errList = append(errList, ValidateMultusNetworkSource(ctx, source.Multus, fieldPath.Child("multus"))...)
	}

	if count == 0 {
		errList = append(errList, field.Required(fieldPath, "at least one network source must be set"))
	}

	if count > 1 {
		errList = append(errList, field.Invalid(fieldPath, source, "only one network source can be set"))
	}

	return errList
}

func ValidatePodNetworkSource(ctx context.Context, source *types.PodNetworkSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	// Pod network source doesn't have required fields currently

	return errList
}

func ValidateMultusNetworkSource(ctx context.Context, source *types.MultusNetworkSource, fieldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	if source.NetworkName == "" {
		errList = append(errList, field.Required(fieldPath.Child("networkName"), "multus network name must be set"))
	}

	return errList
}

func ValidateVMUpdate(ctx context.Context, vm *types.VirtualMachine, oldVM *types.VirtualMachine) field.ErrorList {
	errList := field.ErrorList{}

	// Check if immutable fields are changed
	if vm.Spec.Instance.CPU.Sockets != oldVM.Spec.Instance.CPU.Sockets {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("instance").Child("cpu").Child("sockets"), "CPU sockets cannot be changed"))
	}

	if vm.Spec.Instance.CPU.CoresPerSocket != oldVM.Spec.Instance.CPU.CoresPerSocket {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("instance").Child("cpu").Child("coresPerSocket"), "CPU cores per socket cannot be changed"))
	}

	if !vm.Spec.Instance.Memory.Size.Equal(oldVM.Spec.Instance.Memory.Size) {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("instance").Child("memory").Child("size"), "memory size cannot be changed"))
	}

	if !reflect.DeepEqual(vm.Spec.Instance.Memory.Hugepages, oldVM.Spec.Instance.Memory.Hugepages) {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("instance").Child("memory").Child("hugepages"), "hugepages configuration cannot be changed"))
	}

	if !reflect.DeepEqual(vm.Spec.Instance.Disks, oldVM.Spec.Instance.Disks) {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("instance").Child("disks"), "disks cannot be changed"))
	}

	if !reflect.DeepEqual(vm.Spec.Volumes, oldVM.Spec.Volumes) {
		errList = append(errList, field.Forbidden(field.NewPath("spec").Child("volumes"), "volumes cannot be changed"))
	}

	return errList
}

func generateMAC() (net.HardwareAddr, error) {
	prefix := []byte{0x52, 0x54, 0x00}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("rand: %s", err)
	}
	return net.HardwareAddr(append(prefix, suffix...)), nil
}
