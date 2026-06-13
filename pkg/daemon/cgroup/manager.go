package cgroup

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/configs"

	"github.com/nalajala4naresh/ch-vmm/pkg/daemon/pid"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

type Manager interface {
	Set(ctx context.Context, vm *v1beta1.VirtualMachine, r *configs.Resources) error
}

func NewManager(ctx context.Context, vm *v1beta1.VirtualMachine) (Manager, error) {
	cg := &configs.Cgroup{
		Resources: &configs.Resources{},
	}

	socketPath := filepath.Join("/var/lib/kubelet/pods", string(vm.Status.VMPodUID), "volumes/kubernetes.io~empty-dir/virtink/", "vm.sock")
	if _, err := os.Stat(socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	vmPID, err := pid.GetPIDBySocket(socketPath)
	if err != nil {
		return nil, err
	}
	cgroupPaths, err := cgroups.ParseCgroupFile(fmt.Sprintf("/proc/%v/cgroup", vmPID))
	if err != nil {
		return nil, err
	}

	if cgroups.IsCgroup2UnifiedMode() {
		cgroupPath := cgroupPaths[""]
		err := filepath.Walk("/proc/1/root/sys/fs/cgroup/kubepods.slice", func(path string, info fs.FileInfo, err error) error {
			if !info.IsDir() {
				return nil
			}
			if filepath.Base(path) == filepath.Base(cgroupPath) {
				cgroupPath = path
				return io.EOF
			}
			return nil
		})
		if err != nil && err != io.EOF {
			return nil, err
		}
		return newV2Manager(cg, cgroupPath, vmPID)
	} else {

		panic("Cgroup v1 is not supported")

	}
}
