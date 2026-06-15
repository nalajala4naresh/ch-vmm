# ch-vmm: Lightweight Virtualization Add-on for Kubernetes

[![build](https://github.com/nalajala4naresh/ch-vmm/actions/workflows/build.yml/badge.svg)](https://github.com/nalajala4naresh/ch-vmm/actions/workflows/build.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/nalajala4naresh/ch-vmm)](https://goreportcard.com/report/github.com/nalajala4naresh/ch-vmm)
[![codecov](https://codecov.io/gh/nalajala4naresh/ch-vmm/branch/main/graph/badge.svg?token=6GXYM2BFLT)](https://codecov.io/gh/nalajala4naresh/ch-vmm)

ch-vmm is a [Kubernetes](https://github.com/kubernetes/kubernetes) add-on for running [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) virtual machines. By using Cloud Hypervisor as the underlying hypervisor, ch-vmm enables a lightweight and secure way to run fully virtualized workloads in a canonical Kubernetes cluster.

Compared to [KubeVirt](https://github.com/kubevirt/kubevirt), ch-vmm:

- does not use libvirt or QEMU. By leveraging Cloud Hypervisor, VMs has lower memory (≈30MB) footprints, higher performance and smaller attack surface.
- does not require a long-running per-Pod launcher process, which further reduces runtime memory overhead (≈80MB).

Compared to [VirtInk](https://github.com/smartxworks/virtink), ch-vmm:
- does support snapshot and restore features
- Supports newer version k8s controller-runtime and k8s versions &  cloud-hypervisor v50.0
- `VMPool` and `VMSet` to manage fleet of VM's, checkout docs folder for examples. 

ch-vmm consists of 3 components:

- `ch-vmm-controller` is the cluster-wide controller, responsible for creating Pods to run Cloud Hypervisor VMs.
- `ch-daemon` is the per-Node daemon, responsible for further controlling Cloud Hypervisor VMs on Node bases.
- `virt-prerunner` is the per-Pod pre-runner, responsible for preparing VM networks and building Cloud Hypervisor VM configuration.

**NOTE**: ch-vmm is still a work in progress, its API may change without prior notice.

## Installation

### Requirements

A few requirements need to be met before you can begin:

- Kubernetes Version v1.35+ (In-place vertical scaling of VM with release v1.2.0)
- Kubernetes Version < v1.35 , please use release v1.1.0
- Kubernetes apiserver must have `--allow-privileged=true` in order to run ch-vmm's privileged DaemonSet. It's usually set by default.
- [cert-manager](https://cert-manager.io/)  v1.16 installed in Kubernetes cluster. You can install it with `kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.1/cert-manager.yaml`.
- Deploy ch-vmm onto k8s cluster with ``` kubectl apply -f https://github.com/nalajala4naresh/ch-vmm/releases/latest/download/ch-vmm.yaml```
- Deploy CDI operator to manage DataVolume objects as disks to the VM's 

  ```bash
  $ export VERSION=$(curl -s https://api.github.com/repos/kubevirt/containerized-data-importer/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
  $ kubectl create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$VERSION/cdi-operator.yaml
  $ kubectl create -f https://github.com/kubevirt/containerized-data-importer/releases/download/$VERSION/cdi-cr.yaml
  ```

- Deploy external snapshotter for disk management features
``` bash

$ kubectl -n kube-system kustomize deploy/kubernetes/snapshot-controller | kubectl create -f -
```
#### Container Runtime Support

ch-vmm currently supports the following container runtimes:

- Docker
- containerd
- cloud-hypervisor v50.0 is supported.

Other container runtimes, which do not use virtualization features, should work too. However, they are not tested officially.

#### Hardware Virtualization Support

Hardware with virtualization support is required. You should check if `/dev/kvm` exists on each Kubernetes nodes.

#### Host Kernel Version

- Minimum: v4.11 

## Getting Started

### Create a VM

Apply the following manifest to Kubernetes. Note it uses a [container rootfs](samples/Dockerfile.container-rootfs-ubuntu) and as such doesn’t persist data.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VirtualMachine
metadata:
  name: ubuntu-container-rootfs
spec:
  instance:
    memory:
      size: 1Gi
    kernel:
      image: nalajalanaresh/ch-vmm-kernel:5.15
      cmdline: "console=ttyS0 root=/dev/vda rw"
    disks:
      - name: ubuntu
      - name: cloud-init
    interfaces:
      - name: pod
  volumes:
    - name: ubuntu
      containerRootfs:
        image: nalajalanaresh/virtmanager-container-rootfs-ubuntu
        size: 4Gi
    - name: cloud-init
      cloudInit:
        userData: |-
          #cloud-config
          password: password
          chpasswd: { expire: False }
          ssh_pwauth: True
  networks:
    - name: pod
      pod: {}
EOF
```


Like starting pods, it will take some time to pull the image and start running the VM. You can wait for the VM become running as follows:

```bash
kubectl wait vm ubuntu-container-rootfs --for jsonpath='{.status.phase}'=Running --timeout -1s
```

### Access the VM (via SSH)

The easiest way to access the VM is via a SSH client inside the cluster. You can access the VM created above as follows:

```bash
export VM_NAME=ubuntu-container-rootfs
export VM_POD_NAME=$(kubectl get vm $VM_NAME -o jsonpath='{.status.vmPodName}')
export VM_IP=$(kubectl get pod $VM_POD_NAME -o jsonpath='{.status.podIP}')
kubectl run ssh-$VM_NAME --rm --image=alpine --restart=Never -it -- /bin/sh -c "apk add openssh-client && ssh ubuntu@$VM_IP"
```

Enter `password` when you are prompted to enter password, which is set by the cloud-init data in the VM manifest.


### Take Snapshot of the VM 
ch-vmm supports taking snapshot of the VM's with VMSnapshot CRD, All the Datavolume or Persistent volume based disks use Default storage class VolumeSnapshot class. The memory snapshot is stored in Object storage on both S3 or GCP.
```
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMSnapShot 
metadata:
  name: jammy-snapshot
  namespace: default
spec:
  skipMemorySnapshot: false
  vm: ubuntu-ch
  bucket: gcs://ch-snapshots


```

### Restroing from VM Rollback
ch-vmm supports VM rollback from a VMSnapshot, this will create a new VM but and keep the Original VM untouched.

```

apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMRollback
metadata:
  labels:
    app.kubernetes.io/name: ch-vmm
    app.kubernetes.io/managed-by: kustomize
  name: jammy-ch-rollback3 
spec:
  snapshot: jammy-snapshot


```
### Manage the VM

ch-vmm supports various VM power actions. For example, you can power off the VM created above as follows:

```bash
export VM_NAME=ubuntu-container-rootfs
export POWER_ACTION=PowerOff
kubectl patch vm $VM_NAME --subresource=status --type=merge -p "{\"status\":{\"powerAction\":\"$POWER_ACTION\"}}"
```
You can also `Shutdown`, `Reset`, `Reboot` or `Pause` a running VM, or `Resume` a paused one. To start a powered-off VM, you can `PowerOn` it.

### VFIO GPU Passthrough

ch-vmm supports GPU passthrough to VMs using VFIO (Virtual Function I/O). This allows VMs to directly access physical GPU devices for high-performance workloads such as machine learning, graphics rendering, or GPU-accelerated computing.

#### Prerequisites


- GPU device plugin installed in the cluster (e.g., [NVIDIA Kubevirt Device Plugin](https://github.com/NVIDIA/kubevirt-gpu-device-plugin) or custom device plugin)
- IOMMU enabled on the host system (add `intel_iommu=on` or `amd_iommu=on` to kernel boot parameters)
- GPU devices must be bound to `vfio-pci` driver on the host nodes
- The GPU must be exposed as a Kubernetes extended resource (e.g., `nvidia.com/GP104_GEFORCE_GTX_1080`)

#### Creating a VM with GPU Passthrough

To create a VM with GPU passthrough, specify the GPU in the VM spec:

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VirtualMachine
metadata:
  name: ubuntu-ch
  namespace: default
spec:
  instance:
    kernel:
      image: nalajalanaresh/ch-vmm-kernel:5.15
      cmdline: "console=ttyS0 root=/dev/vda1 rw ds=nocloud"
    memory:
      size: 4Gi
    gpus:
      - name: geforce
        resourceName: nvidia.com/GP104_GEFORCE_GTX_1080  # Change to your GPU resource name
    disks:
      - name: ubuntu
    interfaces:
      - name: pod
  volumes:
    - name: ubuntu
      dataVolume:
        volumeName: ubuntu-ch-jammy
  networks:
    - name: pod
      pod: {}
```

The `resourceName` should match the extended resource name exposed by your GPU device plugin. For NVIDIA GPUs, the webhook automatically sets the `resourceEnvName` if it's not specified. The GPU device will be passed through to the VM using VFIO, allowing the guest OS to directly access the physical GPU hardware.

**Note**: Ensure that the GPU device plugin is properly configured and the GPU resources are available on the target node before creating the VM.

### Relation to Virtink Project

This is a fork of Virtink Project with Custom changes to include additional features

Instead of implementing the ch-vmm controllers from scratch, ch-vmm is importing and sharing code and architecture together with (VirtInk)[https://github.com/smartxworks/virtink/tree/main]
