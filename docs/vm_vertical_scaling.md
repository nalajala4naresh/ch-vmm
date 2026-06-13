# VM Vertical Scaling

This document describes the VM vertical scaling feature in ch-vmm, which allows you to dynamically resize a running VirtualMachine's CPU and memory without downtime.

## Overview

VM vertical scaling leverages Kubernetes 1.35+'s in-place Pod resizing feature to resize the underlying Pod resources, which then triggers the Cloud Hypervisor VM to resize. This enables dynamic resource adjustment of running VMs without requiring a restart.

## Prerequisites

- **Kubernetes 1.35 or later**: Required for in-place Pod resizing support
- **VM must be in `Running` phase**: Vertical scaling only works on running VMs
- **Pod must have resource Requests and Limits**: The mutating webhook automatically sets these from the VM instance spec if not provided

## How It Works

The vertical scaling process follows this flow:

1. **VM CRD Update**: User updates the VM's `spec.instance.cpu` or `spec.instance.memory`
2. **Change Detection**: Controller detects the change by comparing VM spec with Pod's actual resources from `status.containerStatuses[].resources`
3. **Phase Transition**: VM phase transitions to `VirtualMachinePodResizeInProgress`
4. **Pod Resize**: Controller uses the Pod resize subresource to update Pod resources
5. **Pod Resize Completion**: Controller waits for Pod resize to complete (monitoring `status.resize` and `status.containerStatuses[].resources`)
6. **Phase Transition**: VM phase transitions to `VirtualMachineResizeInProgress`
7. **VM Resize**: Daemon resizes the Cloud Hypervisor VM using the `VmResize` API
8. **Completion**: VM phase transitions back to `VirtualMachineRunning`

## VM Phases

### VirtualMachinePodResizeInProgress

This phase indicates that the Pod resize is in progress. The controller:
- Monitors Pod `status.resize` field
- Waits for Pod resize to complete
- Handles infeasible resize conditions
- Transitions to `VirtualMachineResizeInProgress` when Pod resize completes

### VirtualMachineResizeInProgress

This phase indicates that the VM resize is in progress. The daemon:
- Compares current VM resources with desired resources
- Calls Cloud Hypervisor `VmResize` API if needed
- Transitions back to `VirtualMachineRunning` when resize completes

## Resource Calculation

### CPU Resources

- **Desired CPU**: `spec.instance.cpu.sockets * spec.instance.cpu.coresPerSocket`
- **Pod CPU**: Same as desired CPU (no overhead)

### Memory Resources

- **Desired Memory**: `spec.instance.memory.size + 256Mi` (overhead for VM management)
- **Pod Memory**: Same as desired memory

The mutating webhook automatically sets these resources in `spec.resources` if they're not explicitly provided.

## Conditions and Error Handling

### Pod Resize Conditions

#### PodResizePending with Infeasible Reason

When Kubernetes cannot accommodate the requested resource increase on the current node, it sets a condition:

```yaml
conditions:
  - type: PodResizePending
    status: "True"
    reason: Infeasible
    message: 'Node didn''t have enough capacity: memory, requested: 268435456000, capacity: 29877194752'
```

**Handling**: The controller detects this condition and:
- Records a warning event: `VMPodResizeInfeasible`
- Transitions VM back to `VirtualMachineRunning` phase
- Logs the infeasibility reason

#### PodResizeStatusInfeasible

If the Pod's `status.resize` field is set to `Infeasible`:

**Handling**: Same as above - controller transitions back to `Running` and records an event.

### VM Resize Errors

If the Cloud Hypervisor VM resize fails:
- A warning event `VMResizeFailed` is recorded
- The error is logged
- The VM remains in `VirtualMachineResizeInProgress` phase for retry

## Example Usage

### Resizing CPU

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VirtualMachine
metadata:
  name: my-vm
spec:
  instance:
    cpu:
      sockets: 2
      coresPerSocket: 4  # Total: 8 vCPUs
    memory:
      size: 4Gi
```

To increase CPU, update the spec:

```yaml
spec:
  instance:
    cpu:
      sockets: 2
      coresPerSocket: 6  # Total: 12 vCPUs (increased from 8)
```

### Resizing Memory

```yaml
spec:
  instance:
    memory:
      size: 8Gi  # Increased from 4Gi
```

### Combined Resize

You can resize both CPU and memory in a single update:

```yaml
spec:
  instance:
    cpu:
      sockets: 4
      coresPerSocket: 4  # Total: 16 vCPUs
    memory:
      size: 16Gi
```

## Monitoring

### Events

The following events are emitted during vertical scaling:

- **VMResizeInitiated**: When Pod resize is initiated
- **VMResizePodInitiated**: When Pod resize subresource update succeeds
- **VMPodResizeInfeasible**: When Pod resize is infeasible
- **VMResizeInProgress**: When Pod resize completes and VM resize begins
- **VMResizeInitiated**: When Cloud Hypervisor VM resize is initiated
- **VMResizeCompleted**: When VM resize completes successfully
- **VMResizeFailed**: When VM resize fails

### Checking VM Status

```bash
kubectl get vm my-vm -o yaml
```

Look for:
- `status.phase`: Current phase of the VM
- `status.conditions`: Any resize-related conditions

### Checking Pod Status

```bash
kubectl get pod <vm-pod-name> -o yaml
```

Look for:
- `status.resize`: Pod resize status (`InProgress`, `Infeasible`, or empty when complete)
- `status.containerStatuses[].resources`: Actual running resources
- `status.conditions`: Pod resize conditions

## Limitations

1. **Kubernetes Version**: Requires Kubernetes 1.35+ for in-place Pod resizing
2. **Node Capacity**: Resize may fail if the node doesn't have sufficient resources
3. **VM State**: VM must be in `Running` or `Paused` state for resize to proceed
4. **Memory Tolerance**: 5% tolerance is applied when comparing memory to account for rounding
5. **No Downtime Guarantee**: While the feature is designed for in-place resizing, some workloads may experience brief interruptions

## Troubleshooting

### Resize Not Triggering

1. **Check Kubernetes version**: Ensure cluster is running Kubernetes 1.35+
2. **Verify VM phase**: VM must be in `Running` phase
3. **Check Pod resources**: Ensure Pod has resource Requests and Limits set
4. **Review logs**: Check controller logs for resize detection messages

### Resize Failing

1. **Check node capacity**: Verify the node has sufficient resources
2. **Review Pod conditions**: Check for `PodResizePending` with `Infeasible` reason
3. **Check Cloud Hypervisor logs**: Verify VM resize API calls are succeeding
4. **Review events**: Check for `VMResizeFailed` or `VMPodResizeInfeasible` events

### Resize Stuck

If resize appears stuck:
1. Check VM phase - should transition through phases
2. Verify Pod `status.resize` field
3. Check daemon logs for VM resize operations
4. Verify Cloud Hypervisor API is accessible

## Implementation Details

### Controller Responsibilities

- **Change Detection**: `checkAndSetResizePhase()` compares VM spec with Pod actual resources
- **Pod Resize**: `reconcileVMPodResize()` handles Pod resize using resize subresource
- **Status Monitoring**: Monitors Pod `status.resize` and `status.containerStatuses[].resources`

### Daemon Responsibilities

- **VM Resize**: Handles `VirtualMachineResizeInProgress` phase
- **Resource Comparison**: Compares current VM resources with desired resources
- **Cloud Hypervisor API**: Calls `VmResize` API to resize the VM

### Webhook Responsibilities

- **Resource Defaulting**: Mutating webhook sets default CPU and memory resources from VM instance spec
- **Ensures Consistency**: Guarantees Pods always have resource Requests and Limits set

## Related Documentation

- [Kubernetes In-Place Pod Resize](https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources/)
- [Cloud Hypervisor API Documentation](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md)

