package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/sys/mountinfo"
	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/opencontainers/runc/libcontainer/devices"
	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nalajala4naresh/ch-vmm/pkg/cloudhypervisor"
	"github.com/nalajala4naresh/ch-vmm/pkg/daemon/cgroup"
	"github.com/nalajala4naresh/ch-vmm/pkg/daemon/pid"
	"github.com/nalajala4naresh/ch-vmm/pkg/tlsutil"
	"github.com/nalajala4naresh/ch-vmm/pkg/volumeutil"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const (
	vfioMemoryLockSizeBytes int64 = 1 << 30
)

type VMReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	NodeName string
	NodeIP   string
	RelayProvider

	migrationControlBlocks map[types.UID]migrationControlBlock
	mutex                  sync.Mutex
}

// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudhypervisor.quill.today,resources=virtualmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

func (r *VMReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var vm v1beta1.VirtualMachine
	if err := r.Get(ctx, req.NamespacedName, &vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := vm.Status.DeepCopy()

	rerr := r.chReconcile(ctx, &vm)

	if !reflect.DeepEqual(vm.Status, status) {
		if err := r.Status().Update(ctx, &vm); err != nil {
			if rerr == nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("update VM status: %s", err)
			}
		}
	}

	if rerr != nil {
		return ctrl.Result{}, rerr
	}
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *VMReconciler) chReconcile(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	log := ctrl.LoggerFrom(ctx)
	shouldReconcile := (vm.Status.NodeName != "" && vm.Status.NodeName == r.NodeName) ||
		(vm.Status.Migration != nil && vm.Status.Migration.TargetNodeName != "" && vm.Status.Migration.TargetNodeName == r.NodeName)
	if !shouldReconcile {
		return nil
	}

	vmPodKey := types.NamespacedName{
		Name:      vm.Status.VMPodName,
		Namespace: vm.Namespace,
	}
	if vmPodKey.Namespace == "" {
		vmPodKey.Namespace = "default"
	}
	vmPod := &corev1.Pod{}
	if err := r.Get(ctx, vmPodKey, vmPod); err != nil {
		return client.IgnoreNotFound(err)
	}

	if vm.DeletionTimestamp != nil && !vm.DeletionTimestamp.IsZero() {
		if vm.Status.NodeName != r.NodeName {
			return nil
		}
		return r.reconcileDeletingVM(ctx, vm, vmPod)

	}

	//find if this a restore of VM
	var memVolFound bool
	for _, vol := range vm.Spec.Volumes {
		if vol.Name == "memory" {
			memVolFound = true
			break
		}
	}

	switch vm.Status.Phase {
	case v1beta1.VirtualMachineRestoreInProgress:
		//wait for the prerunner to complete populating snapshot directory.
		_, err := os.Stat(filepath.Join(getVMMemoryDataDirPath(vm), "snap", "done.txt"))
		if err != nil {
			r.Recorder.Eventf(vm, corev1.EventTypeWarning, "PrerunnerStage", "waiting on prerunner to create vm snapshot direcory %s", err)
			err = errors.New("waiting prerunner prepare vm snapshot direcory ")
			return err
		}
		chClient := getCloudHypervisorClient(vm)
		err = chClient.VmRestore(ctx, &cloudhypervisor.RestoreConfig{SourceUrl: "file:///mnt/memory/snap"})
		if err != nil {
			r.Recorder.Eventf(vm, corev1.EventTypeWarning, "VMRestoreFailed", fmt.Sprintf("failed to restore vm with error %s", err))
			return err
		}

		//next state for controller to act on
		vm.Status.Phase = v1beta1.VirtualMachineResumed

	case v1beta1.VirtualMachineScheduled:
		if err := r.mountHotplugVolumes(ctx, vm, "", ""); err != nil {
			return err
		}

		if memVolFound {

			vm.Status.Phase = v1beta1.VirtualMachineRestoreInProgress
			return nil

		}

		chClient := getCloudHypervisorClient(vm)
		vmInfo, err := chClient.VmInfo(ctx)
		if err != nil {
			if !isChVMNotCreatedError(err) {
				ctrl.Log.Error(err, "VM not created", "vm", vm.Name)

				return fmt.Errorf("get VM info: %s", err)
			}

			vmConfigFilePath := filepath.Join(getVMDataDirPath(vm), "vm-config.json")
			vmConfigFile, err := os.Open(vmConfigFilePath)
			if err != nil {
				if os.IsNotExist(err) {
					r.Recorder.Eventf(vm, corev1.EventTypeWarning, "PrerunnerStage", "waiting on prerunner to create VM config")
					err = errors.New("waiting prerunner prepare VM config")
					return err
				}
				return err
			}
			//read vmconfig
			var vmConfig cloudhypervisor.VmConfig
			if err := json.NewDecoder(vmConfigFile).Decode(&vmConfig); err != nil {
				ctrl.Log.Error(err, "failed to decode ch vm config", "vm", vm.Name, "configpath", vmConfigFilePath)
				return err
			}

			//get process id firecracker or cloud hypervisor
			hypervisorPID, err := pid.GetPIDBySocket(filepath.Join(getVMDataDirPath(vm), "vm.sock"))
			if err != nil {
				ctrl.Log.Error(err, "failed to get pid by socket", "vm", vm.Name, "configpath", getVMDataDirPath(vm))
				return fmt.Errorf("get hypervisor process pid: %s", err)
			}

			if len(vmConfig.Devices) > 0 || len(vmConfig.Vdpa) > 0 {

				rlimit := &unix.Rlimit{
					Cur: uint64(vmConfig.Memory.Size + vfioMemoryLockSizeBytes),
					Max: uint64(vmConfig.Memory.Size + vfioMemoryLockSizeBytes),
				}
				if err := unix.Prlimit(hypervisorPID, unix.RLIMIT_MEMLOCK, rlimit, &unix.Rlimit{}); err != nil {
					return fmt.Errorf("set cloud-hypervisor process memlock: %s", err)
				}
			}

			if err := chClient.VmCreate(ctx, &vmConfig); err != nil {
				ctrl.Log.Error(err, "vm create failed", "vm", vm.Name)
				return fmt.Errorf("create VM: %s", err)
			}
			ctrl.Log.Info("VM Created", "vm", vm.Name)
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMCreated", "Cloud Hypervisor VM Created")
			if err := chClient.VmBoot(ctx); err != nil {
				ctrl.Log.Error(err, "vm boot failed", "vm", vm.Name)
				return err
			}
			ctrl.Log.Info("VM Booted", "vm", vm.Name)
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMBooted", "Cloud Hypervisor VM Booted")
		} else {
			switch vmInfo.State {
			case "Created":
				if err := chClient.VmBoot(ctx); err != nil {
					ctrl.Log.Error(err, "vm boot failed while vm is in created state", "vm", vm.Name)

					return err
				}
				ctrl.Log.Info("VM Booted", "vm", vm.Name)
				r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMBooted", "Cloud Hypervisor VM Booted")
			case "Running":
				vm.Status.Phase = v1beta1.VirtualMachineRunning
			case "Paused":
				vm.Status.Phase = v1beta1.VirtualMachinePaused
			case "Shutdown":
				vm.Status.Phase = v1beta1.VirtualMachineFailed
			}
		}
	case v1beta1.VirtualMachinePaused:
		if vm.Status.Migration == nil {
			if vm.Status.NodeName != r.NodeName {
				return nil
			}
			vmInfo, err := getCloudHypervisorClient(vm).VmInfo(ctx)
			if err != nil {
				// TODO: ignore VM not found error
				return fmt.Errorf("get VM info: %s", err)
			}
			if vmInfo.State == "Running" {
				if err := getCloudHypervisorClient(vm).VmPause(ctx); err != nil {
					r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMPause", "Failed to pause VM")
				} else {
					r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMPaused", "Cloud Hypervisor VM Paused")
				}

			}

		}

	case v1beta1.VirtualMachineResumed:
		if vm.Status.Migration == nil {
			if vm.Status.NodeName != r.NodeName {
				return nil
			}
			vmInfo, err := getCloudHypervisorClient(vm).VmInfo(ctx)
			if err != nil {
				// TODO: ignore VM not found error
				return fmt.Errorf("get VM info: %s", err)
			}
			if vmInfo.State == "Paused" {
				if err := getCloudHypervisorClient(vm).VmResume(ctx); err != nil {
					r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedResume", "Failed to pause VM")
					return err

				} else {
					r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResumed", "Cloud Hypervisor VM Resumed")
				}

				vm.Status.Phase = v1beta1.VirtualMachineRunning

			}

		}

	case v1beta1.VirtualMachineSnapShotInprogress:
		// do not do anything as vm will go through pause and resume life cycle

	case v1beta1.VirtualMachineResizeInProgress:
		// Handle VM resize after Pod resize is complete
		if vm.Status.Migration == nil {
			if vm.Status.NodeName != r.NodeName {
				return nil
			}
			chClient := getCloudHypervisorClient(vm)
			vmInfo, err := chClient.VmInfo(ctx)
			if err != nil {
				return fmt.Errorf("get VM info: %s", err)
			}

			// Only resize if VM is Running or Paused
			if vmInfo.State != "Running" && vmInfo.State != "Paused" {
				ctrl.Log.Info("VM resize: VM is not in Running or Paused state, waiting",
					"vm", vm.Name, "state", vmInfo.State)
				return nil
			}

			// Calculate desired resources from VM spec
			desiredVcpus := int(vm.Spec.Instance.CPU.Sockets * vm.Spec.Instance.CPU.CoresPerSocket)
			desiredRam := vm.Spec.Instance.Memory.Size.Value() // Size is in bytes

			// Get current VM resources
			currentVcpus := vmInfo.Config.Cpus.BootVcpus
			currentRam := vmInfo.MemoryActualSize // This is the actual memory in bytes

			// Check if resize is needed
			needsResize := false
			if currentVcpus != desiredVcpus {
				needsResize = true
				ctrl.Log.Info("VM resize: CPU change detected",
					"vm", vm.Name, "current", currentVcpus, "desired", desiredVcpus)
			}

			// Allow 5% tolerance for memory to account for rounding
			memoryTolerance := int64(float64(desiredRam) * 0.05)
			memoryDiff := currentRam - desiredRam
			if memoryDiff < 0 {
				memoryDiff = -memoryDiff
			}
			if memoryDiff > memoryTolerance {
				needsResize = true
				ctrl.Log.Info("VM resize: Memory change detected",
					"vm", vm.Name, "current", currentRam, "desired", desiredRam)
			}

			if needsResize {
				resizeReq := &cloudhypervisor.VmResize{
					DesiredVcpus: desiredVcpus,
					DesiredRam:   desiredRam,
				}

				if err := chClient.VmResize(ctx, resizeReq); err != nil {
					r.Recorder.Eventf(vm, corev1.EventTypeWarning, "VMResizeFailed",
						"Failed to resize VM: %s", err)
					return fmt.Errorf("resize VM: %s", err)
				}

				ctrl.Log.Info("VM resize: Resize request sent to Cloud Hypervisor",
					"vm", vm.Name, "vcpus", desiredVcpus, "ram", desiredRam)
				r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResizeInitiated",
					"VM resize initiated: vCPUs=%d, RAM=%d bytes", desiredVcpus, desiredRam)
				// Will be checked in next reconcile to verify resize completed
				return nil
			}

			// Resize is complete, transition back to Running
			vm.Status.Phase = v1beta1.VirtualMachineRunning
			ctrl.Log.Info("VM resize: Resize completed, transitioning to Running",
				"vm", vm.Name)
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMResizeCompleted",
				"VM resize completed successfully")
		}

	case v1beta1.VirtualMachineRunning:
		if vm.Status.Migration == nil {
			if vm.Status.NodeName != r.NodeName {
				return nil
			}
			vmInfo, err := getCloudHypervisorClient(vm).VmInfo(ctx)
			if err != nil {
				// TODO: ignore VM not found error
				return fmt.Errorf("get VM info: %s", err)
			}

			if vmInfo.State == "Running" || vmInfo.State == "Paused" {
				if vm.Spec.RunPolicy == v1beta1.RunPolicyHalted {
					// TODO: shutdown with graceful timeout
					if err := getCloudHypervisorClient(vm).VmShutdown(ctx); err != nil {

						r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMPowerOff", "Failed to powered off VM")
						return fmt.Errorf("power off VM: %s", err)
					}
				} else if vm.Status.PowerAction != "" {
					switch vm.Status.PowerAction {
					case v1beta1.VirtualMachinePowerOff:
						if err := getCloudHypervisorClient(vm).VmPowerButton(ctx); err != nil {
							r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMPowerOff", "Failed to powered off VM")
						} else {
							r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMPoweredOff", "Cloud Hypervisor VM PowerdOff")
						}
					case v1beta1.VirtualMachineShutdown:
						if err := getCloudHypervisorClient(vm).VmShutdown(ctx); err != nil {
							r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMShutdown", "Failed to shutdown VM")
						} else {
							r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMShutdown", "Cloud Hypervisor VM Shutdown ")
						}
					case v1beta1.VirtualMachineReset:
						if err := getCloudHypervisorClient(vm).VmReboot(ctx); err != nil {
							r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMReset", "Failed to reset VM")
						} else {
							r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMReset", "Cloud Hypervisor VM Reset")
						}
					case v1beta1.VirtualMachineReboot:
						// TODO: reboot
						if err := getCloudHypervisorClient(vm).VmReboot(ctx); err != nil {
							r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMReboot", "Failed to reboot VM")
						} else {
							r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMRebooted", "Cloud Hypervisor VM Rebooted")
						}
					default:
						// ignored
					}

					vm.Status.PowerAction = ""
					return nil
				}

				if err := r.reconcileHotplugVolumes(ctx, vm, vmInfo); err != nil {
					return err
				}
			} else {
				vm.Status.Phase = v1beta1.VirtualMachineSucceeded
			}
		} else {
			r.mutex.Lock()
			defer r.mutex.Unlock()
			migrationControlBlock := r.migrationControlBlocks[vm.UID]

			var daemonCertDirPath = "/var/lib/virtmanager/daemon/cert"
			switch vm.Status.Migration.Phase {
			case v1beta1.VirtualMachineMigrationScheduled:
				if vm.Status.Migration.TargetNodeName == r.NodeName {
					if err := wait.PollImmediate(time.Second, 3*time.Second, func() (done bool, err error) {
						if _, err := os.Stat(filepath.Join(getMigrationTargetVMSocketDirPath(vm), "vm.sock")); err != nil {
							if !os.IsNotExist(err) {
								return false, err
							}
							return false, nil
						}
						return true, nil
					}); err != nil {
						r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMMigrate", "timeout for waiting cloud-hypervisor be running")
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
						return nil
					}
					ctx, cancel := context.WithCancel(context.Background())
					migrationControlBlock.ReceiveMigrationCancelFunc = cancel

					receiveMigrationSocketPath := filepath.Join(getMigrationTargetVMSocketDirPath(vm), "rx.sock")
					if _, err := os.Stat(receiveMigrationSocketPath); err != nil {
						if !os.IsNotExist(err) {
							return err
						}
						receiveMigrationErrChan := make(chan error, 1)
						go func() {
							if err := r.getMigrationTargetCloudHypervisorClient(vm).VmReceiveMigration(ctx, &cloudhypervisor.ReceiveMigrationData{
								ReceiverUrl: "unix:/var/run/ch-vmm/rx.sock",
							}); err != nil {
								receiveMigrationErrChan <- err
							}
						}()
						migrationControlBlock.ReceiveMigrationErrCh = receiveMigrationErrChan
						var condition wait.ConditionFunc = func() (done bool, err error) {
							select {
							case err = <-migrationControlBlock.ReceiveMigrationErrCh:
								return false, err
							default:
								if _, err := os.Stat(receiveMigrationSocketPath); err != nil {
									if os.IsNotExist(err) {
										return false, nil
									}
									return false, err
								}
								return true, nil
							}
						}

						if err := wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Second, true, condition.WithContext()); err != nil {
							r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMMigrate", "Failed to receive migration on %s: %s", vm.Status.Migration.TargetNodeName, err)
							vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
							return nil
						}
					}

					clientCACertPool, err := tlsutil.LoadCACert(daemonCertDirPath)
					if err != nil {
						return fmt.Errorf("load CA cert: %s", err)
					}
					tlsConfig := &tls.Config{
						GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
							return tlsutil.LoadCert(daemonCertDirPath)
						},
						ClientAuth: tls.RequireAndVerifyClientCert,
						ClientCAs:  clientCACertPool,
					}

					port, err := r.RelayTCPToSocket(ctx, "0.0.0.0:0", tlsConfig, receiveMigrationSocketPath)
					if err != nil {
						return fmt.Errorf("start target relay: %s", err)
					}

					if err := r.mountHotplugVolumes(ctx, vm, vm.Status.Migration.TargetVMPodUID, vm.Status.Migration.TargetVolumePodUID); err != nil {
						return err
					}

					vm.Status.Migration.TargetNodePort = port
					vm.Status.Migration.TargetNodeIP = r.NodeIP
					vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationTargetReady
				}
			case v1beta1.VirtualMachineMigrationTargetReady:
				if vm.Status.NodeName == r.NodeName {
					ctx, cancel := context.WithCancel(context.Background())
					migrationControlBlock.SendMigrationCancelFunc = cancel

					tlsConfig := &tls.Config{
						InsecureSkipVerify: true,
						GetClientCertificate: func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
							return tlsutil.LoadCert(daemonCertDirPath)
						},
					}
					if err := r.RelaySocketToTCP(ctx, filepath.Join(getVMDataDirPath(vm), "tx.sock"), fmt.Sprintf("%s:%d", vm.Status.Migration.TargetNodeIP, vm.Status.Migration.TargetNodePort), tlsConfig); err != nil {
						return fmt.Errorf("start source relay: %s", err)
					}

					sendMigrationErrChan := make(chan error, 1)
					go func() {
						if err := getCloudHypervisorClient(vm).VmSendMigration(ctx, &cloudhypervisor.SendMigrationData{
							DestinationUrl: "unix:/var/run/ch-vmm/tx.sock",
						}); err != nil {
							sendMigrationErrChan <- err
						}
					}()
					migrationControlBlock.SendMigrationErrCh = sendMigrationErrChan
					vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationRunning
				}
			case v1beta1.VirtualMachineMigrationRunning:
				if vm.Status.NodeName == r.NodeName {
					if vmPod != nil && vmPod.Status.Phase == corev1.PodSucceeded {
						if err := r.cleanup(ctx, vm); err != nil {
							return err
						}
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationSent
					} else if migrationControlBlock.SendMigrationErrCh == nil {
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
					} else {
						select {
						case err := <-migrationControlBlock.SendMigrationErrCh:
							if err != nil {
								r.Recorder.Eventf(vm, corev1.EventTypeWarning, "FailedVMMigrate", "Failed to migrate VM to %s: %s", vm.Status.Migration.TargetNodeName, err)
								vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationFailed
							}
						default:
							log.Info("VM is sending migration")
							return nil
						}
					}
					if sendDomainCancelFunc := migrationControlBlock.SendMigrationCancelFunc; sendDomainCancelFunc != nil {
						sendDomainCancelFunc()
					}
				}
			case v1beta1.VirtualMachineMigrationSent:
				if vm.Status.Migration.TargetNodeName == r.NodeName {
					timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
					defer cancel()
					vmInfo, err := r.getMigrationTargetCloudHypervisorClient(vm).VmInfo(timeoutCtx)
					if err != nil {
						return err
					}
					switch vmInfo.State {
					case "Running":
						vm.Status.Migration.Phase = v1beta1.VirtualMachineMigrationSucceeded
						vm.Status.NodeName = vm.Status.Migration.TargetNodeName
						vm.Status.VMPodName = vm.Status.Migration.TargetVMPodName
						vm.Status.VMPodUID = vm.Status.Migration.TargetVMPodUID
					default:
						log.Info("waiting target VM being Running")
						return nil
					}

					if receiveDomainCancelFunc := migrationControlBlock.ReceiveMigrationCancelFunc; receiveDomainCancelFunc != nil {
						receiveDomainCancelFunc()
					}
				}
			case v1beta1.VirtualMachineMigrationSucceeded, v1beta1.VirtualMachineMigrationFailed:
				// TODO when failed VM may running on source or target node, need check it and clean up
				delete(r.migrationControlBlocks, vm.UID)
			}

			r.migrationControlBlocks[vm.UID] = migrationControlBlock
			if vm.Status.Migration.Phase == v1beta1.VirtualMachineMigrationFailed ||
				vm.Status.Migration.Phase == v1beta1.VirtualMachineMigrationSucceeded ||
				(vm.Status.Migration.Phase == v1beta1.VirtualMachineMigrationSent && vm.Status.NodeName == r.NodeName) {
				delete(r.migrationControlBlocks, vm.UID)
			}
		}
	case v1beta1.VirtualMachineSucceeded, v1beta1.VirtualMachineFailed:
		if err := r.cleanup(ctx, vm); err != nil {
			return err
		}
	}
	return nil
}

func getCloudHypervisorClient(vm *v1beta1.VirtualMachine) *cloudhypervisor.Client {
	ctrl.Log.Info("Get VM Client", "vm", vm.Name, "datapath", getVMDataDirPath(vm))
	return cloudhypervisor.NewClient(filepath.Join(getVMDataDirPath(vm), "vm.sock"))
}

func getVMDataDirPath(vm *v1beta1.VirtualMachine) string {
	return filepath.Join("/pods", string(vm.Status.VMPodUID), "volumes/kubernetes.io~empty-dir/ch-vmm/")
}

func getVMMemoryDataDirPath(vm *v1beta1.VirtualMachine) string {
	return filepath.Join("/pods", string(vm.Status.VMPodUID), "volumes/kubernetes.io~empty-dir/memory/")
}

func (r *VMReconciler) getMigrationTargetCloudHypervisorClient(vm *v1beta1.VirtualMachine) *cloudhypervisor.Client {
	return cloudhypervisor.NewClient(filepath.Join(getMigrationTargetVMSocketDirPath(vm), "vm.sock"))
}
func getMigrationTargetVMSocketDirPath(vm *v1beta1.VirtualMachine) string {
	return filepath.Join("/pods", string(vm.Status.Migration.TargetVMPodUID), "volumes/kubernetes.io~empty-dir/ch-vmm/")
}

func (r *VMReconciler) reconcileHotplugVolumes(ctx context.Context, vm *v1beta1.VirtualMachine, vmInfo *cloudhypervisor.VmInfo) error {
	if err := r.mountHotplugVolumes(ctx, vm, "", ""); err != nil {
		return err
	}

	if err := r.addHotplugVolumesToVM(ctx, vm, vmInfo); err != nil {
		return err
	}

	if err := r.removeHotplugVolumesFromVM(ctx, vm, vmInfo); err != nil {
		return err
	}

	if err := r.umountHotplugVolumes(ctx, vm); err != nil {
		return err
	}
	return nil
}

func (r *VMReconciler) mountHotplugVolumes(ctx context.Context, vm *v1beta1.VirtualMachine, vmPodUID types.UID, volumePodUID types.UID) error {
	if vmPodUID == types.UID("") {
		vmPodUID = vm.Status.VMPodUID
	}

	record, err := getVMMountRecord(vm.UID)
	if err != nil {
		ctrl.Log.Error(err, "Get VM Mount record failed", "vm", vm.Name)
		return err
	}

	for _, volume := range vm.Spec.Volumes {
		if !volume.IsHotpluggable() {
			continue
		}

		var volumeStatus *v1beta1.VolumeStatus
		for i := range vm.Status.VolumeStatus {
			if vm.Status.VolumeStatus[i].Name == volume.Name {
				volumeStatus = &vm.Status.VolumeStatus[i]
				break
			}
		}
		if volumeStatus == nil || volumeStatus.HotplugVolume == nil {
			continue
		}
		if volumePodUID != types.UID("") || volumeStatus.Phase == v1beta1.VolumeAttachedToNode {
			if volumePodUID == types.UID("") {
				volumePodUID = volumeStatus.HotplugVolume.VolumePodUID
			}

			isBlock, err := volumeutil.IsBlock(ctx, r.Client, vm.Namespace, *volume)
			if err != nil {
				return err
			}
			if isBlock {
				if err := r.mountBlockVolume(ctx, vm, volume.Name, vmPodUID, volumePodUID, record); err != nil {
					return err
				}
			} else {
				if err := r.mountFileSystemVolume(ctx, vm, volume.Name, vmPodUID, volumePodUID, record); err != nil {
					return err
				}
			}
		}
		if volumeStatus.Phase == v1beta1.VolumeAttachedToNode {
			volumeStatus.Phase = v1beta1.VolumeMountedToPod
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "MountVolumeToPod", "Mounted volume %s to VM pod", volumeStatus.Name)
		}
	}
	return nil
}

func (r *VMReconciler) mountBlockVolume(ctx context.Context, vm *v1beta1.VirtualMachine, volume string, vmPodUID types.UID, volumePodUID types.UID, record *vmMountRecord) error {
	target := filepath.Join("/pods", string(vmPodUID), "volumes/kubernetes.io~empty-dir/hotplug-volumes/", volume)
	mounted := true
	if _, err := os.Stat(target); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		mounted = false
	}

	source := filepath.Join("/pods", string(volumePodUID), "volumes/kubernetes.io~empty-dir/hotplug/", volume)
	major, minor, _, err := getBlockFileMajorMinor(source)
	if err != nil {
		return err
	}
	if err := addVolumeToVMMountRecord(vm.UID, volume, target, record); err != nil {
		return err
	}

	if !mounted {
		if _, err := executeCommand("/bin/mknod", target, "b", strconv.FormatInt(major, 10), strconv.FormatInt(minor, 10)); err != nil {
			return fmt.Errorf("mknod: %s", err)
		}
	}

	cgroupManager, err := cgroup.NewManager(ctx, vm)
	if err != nil {
		return err
	}
	if err := cgroupManager.Set(ctx, vm, &configs.Resources{
		Devices: []*devices.Rule{{
			Type:        devices.BlockDevice,
			Major:       major,
			Minor:       minor,
			Permissions: "rwm",
			Allow:       true,
		}},
	}); err != nil {
		return fmt.Errorf("set cgroup for volume device %s: %s", volume, err)
	}
	return nil
}

func (r *VMReconciler) mountFileSystemVolume(ctx context.Context, vm *v1beta1.VirtualMachine, volume string, vmPodUID types.UID, volumePodUID types.UID, record *vmMountRecord) error {
	target := fmt.Sprintf("/pods/%s/volumes/kubernetes.io~empty-dir/hotplug-volumes/%s.img", vmPodUID, volume)
	mounted, err := isMounted(target)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}

	source, err := getHotplugVolumeSourcePathOnHost(volume, string(volumePodUID))
	if err != nil {
		return err
	}
	if err := addVolumeToVMMountRecord(vm.UID, volume, target, record); err != nil {
		return err
	}
	if _, err := os.Stat(target); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		f, err := os.Create(target)
		if err != nil {
			return err
		}
		f.Close()
	}

	if _, err := executeCommand("mount", "-o", "bind", filepath.Join(source, "disk.img"), target); err != nil {
		return fmt.Errorf("mount volume %s: %s", volume, err)
	}
	return nil
}

func getBlockFileMajorMinor(filePath string) (int64, int64, string, error) {
	output, err := exec.Command("/bin/stat", filePath, "-L", "-c%t,%T,%a,%F").CombinedOutput()
	if err != nil {
		return -1, -1, "", fmt.Errorf("/bin/stat %s: %s", output, err)
	}
	split := strings.Split(string(output), ",")
	if len(split) != 4 {
		return -1, -1, "", errors.New("output is invalid")
	}
	major, err := strconv.ParseInt(split[0], 16, 32)
	if err != nil {
		return -1, -1, "", err
	}
	minor, err := strconv.ParseInt(split[1], 16, 32)
	if err != nil {
		return -1, -1, "", err
	}
	return major, minor, split[2], nil
}

func (r *VMReconciler) umountHotplugVolumes(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	record, err := getVMMountRecord(vm.UID)
	if err != nil {
		return err
	}

	volumeStatusMap := map[string]v1beta1.VolumeStatus{}
	for _, status := range vm.Status.VolumeStatus {
		volumeStatusMap[status.Name] = status
	}

	newRecord := &vmMountRecord{}
	for _, volumeRecord := range record.Volumes {
		if _, ok := volumeStatusMap[volumeRecord.Volume]; ok {
			newRecord.Volumes = append(newRecord.Volumes, volumeRecord)
			continue
		}
		if err := r.umountHotplugVolume(ctx, vm, &volumeRecord); err != nil {
			return err
		}
	}
	if len(newRecord.Volumes) > 0 {
		if err := writeVMMountRecord(vm.UID, newRecord); err != nil {
			return err
		}
	} else {
		if err := removeVMMountRecord(vm.UID); err != nil {
			return err
		}
	}
	return nil
}

func (r *VMReconciler) umountAllHotplugVolumes(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	record, err := getVMMountRecord(vm.UID)
	if err != nil {
		return err
	}
	for _, volumeRecord := range record.Volumes {
		if err := r.umountHotplugVolume(ctx, vm, &volumeRecord); err != nil {
			return err
		}
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "UmountVolumeFromPod", "Umount volume %s from VM pod", volumeRecord.Volume)
	}

	if err := removeVMMountRecord(vm.UID); err != nil {
		return err
	}
	return nil
}

func (r *VMReconciler) umountHotplugVolume(ctx context.Context, vm *v1beta1.VirtualMachine, volumeRecord *volumeMountRecord) error {
	if isBlockFile(volumeRecord.Target) {
		if _, err := os.Stat(volumeRecord.Target); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		major, minor, _, err := getBlockFileMajorMinor(volumeRecord.Target)
		if err != nil {
			return err
		}
		cgroupManager, err := cgroup.NewManager(ctx, vm)
		if err != nil {
			return err
		}
		if cgroupManager != nil {
			if err := cgroupManager.Set(ctx, vm, &configs.Resources{
				Devices: []*devices.Rule{{
					Type:        devices.BlockDevice,
					Major:       major,
					Minor:       minor,
					Permissions: "rwm",
					Allow:       false,
				}},
			}); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(volumeRecord.Target); err != nil {
			return err
		}
	} else {
		mounted, err := isMounted(volumeRecord.Target)
		if err != nil {
			return err
		}
		if mounted {
			if _, err := executeCommand("umount", volumeRecord.Target); err != nil {
				return fmt.Errorf("umount volume from VM pod: %s", err)
			}
		}
	}
	r.Recorder.Eventf(vm, corev1.EventTypeNormal, "UmountVolumeFromPod", "Umount volume %s from VM pod", volumeRecord.Volume)
	return nil
}

func (r *VMReconciler) addHotplugVolumesToVM(ctx context.Context, vm *v1beta1.VirtualMachine, vmInfo *cloudhypervisor.VmInfo) error {
	vmDisksMap := map[string]*cloudhypervisor.DiskConfig{}
	for _, disk := range vmInfo.Config.Disks {
		vmDisksMap[disk.Id] = disk
	}

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
		if volumeStatus == nil {
			continue
		}

		switch volumeStatus.Phase {
		case v1beta1.VolumeMountedToPod:
			if _, ok := vmDisksMap[volumeStatus.Name]; !ok {
				diskConfig := &cloudhypervisor.DiskConfig{
					Id:     volumeStatus.Name,
					Direct: true,
				}

				isBlock, err := volumeutil.IsBlock(ctx, r.Client, vm.Namespace, *volume)
				if err != nil {
					return err
				}

				if isBlock {
					diskConfig.Path = filepath.Join("/hotplug-volumes", volumeStatus.Name)
				} else {
					diskConfig.Path = filepath.Join("/hotplug-volumes", fmt.Sprintf("%s.img", volumeStatus.Name))
				}
				if _, err := getCloudHypervisorClient(vm).VmAddDisk(ctx, diskConfig); err != nil {
					return fmt.Errorf("add disk: %s", err)
				}
				r.Recorder.Eventf(vm, corev1.EventTypeNormal, "AddDiskToVM", "Added disk %s to VM", volumeStatus.Name)
			}
			volumeStatus.Phase = v1beta1.VolumeReady
		}
	}
	return nil
}

func (r *VMReconciler) removeHotplugVolumesFromVM(ctx context.Context, vm *v1beta1.VirtualMachine, vmInfo *cloudhypervisor.VmInfo) error {
	vmVolumesSet := map[string]bool{}
	for _, volume := range vm.Spec.Volumes {
		vmVolumesSet[volume.Name] = true
	}

	for _, disk := range vmInfo.Config.Disks {
		if !vmVolumesSet[disk.Id] {
			if err := getCloudHypervisorClient(vm).VmRemoveDevice(ctx, &cloudhypervisor.VmRemoveDevice{Id: disk.Id}); err != nil {
				return fmt.Errorf("remove disk from VM: %s", err)
			}
			r.Recorder.Eventf(vm, corev1.EventTypeNormal, "RemoveDiskFromVM", "Remove disk %s from VM", disk.Id)
		}
	}
	return nil
}

func getHotplugVolumeSourcePathOnHost(volume string, volumePodUID string) (string, error) {
	pid, err := pid.GetPIDBySocket(fmt.Sprintf("/pods/%s/volumes/kubernetes.io~empty-dir/hotplug/hp.sock", volumePodUID))
	if err != nil {
		return "", err
	}

	mounts, err := getMountInfos(pid, mountinfo.SingleEntryFilter("/mnt/"+volume))
	if err != nil {
		return "", err
	}
	if len(mounts) == 0 {
		return "", fmt.Errorf("no mount info for %s in volume pod", volume)
	}

	mountsOnHost, err := getMountInfos(1, func(m *mountinfo.Info) (bool, bool) {
		// TODO if add more than one volumes at the same time with same major and minor will found wrong source path
		return m.Major != mounts[0].Major || m.Minor != mounts[0].Minor || !strings.HasPrefix(mounts[0].Root, m.Root) || !strings.Contains(m.Mountpoint, volumePodUID), false
	})
	if err != nil {
		return "", err
	}

	if len(mountsOnHost) == 0 {
		return "", fmt.Errorf("n mount info for %s on host", volume)
	}
	return mountsOnHost[0].Mountpoint, nil
}

func getVMMountRecordFile(vmUID types.UID) string {
	return filepath.Join("/var/run/ch-vmm/hotplug-volume-mount-record", string(vmUID))
}

func getVMMountRecord(uid types.UID) (*vmMountRecord, error) {
	path := getVMMountRecordFile(uid)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return &vmMountRecord{}, nil
		}
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var record vmMountRecord
	if err := json.NewDecoder(f).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func addVolumeToVMMountRecord(vmUID types.UID, volume string, target string, record *vmMountRecord) error {
	for _, volumeRecord := range record.Volumes {
		if volumeRecord.Volume == volume {
			return nil
		}
	}
	record.Volumes = append(record.Volumes, volumeMountRecord{
		Volume: volume,
		Target: target,
	})

	return writeVMMountRecord(vmUID, record)
}

func writeVMMountRecord(vmUID types.UID, record *vmMountRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	path := getVMMountRecordFile(vmUID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return nil
}

func removeVMMountRecord(vmUID types.UID) error {
	return os.RemoveAll(getVMMountRecordFile(vmUID))
}

func isMounted(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return mountinfo.Mounted(path)
}

func getMountInfos(pid int, filter mountinfo.FilterFunc) ([]*mountinfo.Info, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/mountinfo", pid))
	if err != nil {
		return nil, err
	}
	return mountinfo.GetMountsFromReader(f, filter)
}

func (r *VMReconciler) cleanup(ctx context.Context, vm *v1beta1.VirtualMachine) error {
	return r.umountAllHotplugVolumes(ctx, vm)
}

func (r *VMReconciler) reconcileDeletingVM(ctx context.Context, vm *v1beta1.VirtualMachine, vmPod *corev1.Pod) error {

	if vmPod != nil && vmPod.Status.Phase == corev1.PodRunning && vmPod.DeletionTimestamp.IsZero() {

		if err := getCloudHypervisorClient(vm).VmDelete(ctx); err != nil {
			ctrl.Log.Info("daemon reconcling VM  and VM delete error ", "err", err.Error())
			if !isChVMNotCreatedError(err) {
				return err
			}

		}
		r.Recorder.Eventf(vm, corev1.EventTypeNormal, "VMDeleted", "CloudHypervsior VM Deleted")

	}
	vm.Status.Phase = v1beta1.VirtualMachineSucceeded

	if err := r.cleanup(ctx, vm); err != nil {
		return err
	}

	return nil
}

func executeCommand(name string, arg ...string) (string, error) {
	cmd := exec.Command(name, arg...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%q: %s: %s", cmd.String(), err, output)
	}
	return string(output), nil
}

func (r *VMReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.migrationControlBlocks = map[types.UID]migrationControlBlock{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachine{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

//go:generate mockgen -destination=mock/relay_provider.go -package=mock . RelayProvider

type RelayProvider interface {
	RelaySocketToTCP(ctx context.Context, socketPath string, tcpAddr string, tlsConfig *tls.Config) error
	RelayTCPToSocket(ctx context.Context, tcpAddr string, tlsConfig *tls.Config, socketPath string) (int, error)
}

type migrationControlBlock struct {
	SendMigrationErrCh         <-chan error
	SendMigrationCancelFunc    context.CancelFunc
	ReceiveMigrationErrCh      <-chan error
	ReceiveMigrationCancelFunc context.CancelFunc
}

type vmMountRecord struct {
	Volumes []volumeMountRecord `json:"volumes,omitempty"`
}

type volumeMountRecord struct {
	Volume string `json:"volume"`
	Target string `json:"target"`
}

func isChVMNotCreatedError(err error) bool {
	return strings.Contains(err.Error(), "VM is not created")
}

func isBlockFile(filePath string) bool {
	output, err := exec.Command("/usr/bin/stat", filePath, "-L", "-c%F").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "block special file"
}
