package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/docker/libnetwork/resolvconf"
	"github.com/docker/libnetwork/types"
	userspacecni "github.com/intel/userspace-cni-network-plugin/pkg/types"
	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/namsral/flag"
	"github.com/subgraph/libmacouflage"
	"github.com/vishvananda/netlink"

	"github.com/nalajala4naresh/ch-vmm/pkg/cloudhypervisor"
	"github.com/nalajala4naresh/ch-vmm/pkg/cloudutils"
	"github.com/nalajala4naresh/ch-vmm/pkg/cpuset"
	"github.com/nalajala4naresh/ch-vmm/pkg/volumeutil"
	"github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const AGENT_VSOCK = "/var/run/ch-vmm/agent.vsock"
const SERIAL_SOCK = "/var/run/ch-vmm/serial.sock"

const AGENT_CID = 3

func main() {
	var vmData string
	var receiveMigration bool
	flag.StringVar(&vmData, "vm-data", vmData, "Base64 encoded VM json data")
	flag.BoolVar(&receiveMigration, "receive-migration", receiveMigration, "Receive migration instead of starting a new VM")
	flag.Parse()

	vmJSON, err := base64.StdEncoding.DecodeString(vmData)
	if err != nil {
		log.Fatalf("Failed to decode VM data: %s", err)
	}

	var vm v1beta1.VirtualMachine
	if err := json.Unmarshal(vmJSON, &vm); err != nil {
		log.Fatalf("Failed to unmarshal VM: %s", err)
	}

	var memoryVolFound bool
	var memVol v1beta1.Volume
	for _, vol := range vm.Spec.Volumes {
		if vol.MemorySnapshot != nil {
			memoryVolFound = true
			memVol = *vol

			break

		}
	}

	if memoryVolFound {
		err := getMemorySnapshot(&vm, memVol)

		if err != nil {
			log.Fatalln(fmt.Sprintf("Unable to get memory snapshot with error %s", err))
		}
	}

	//we build VMConfig all the time  to account for  initial restore and subsequent reboots

	vmConfig, err := buildChVMConfig(context.Background(), &vm)

	if err != nil {
		log.Fatalf("Failed to build VM config: %s", err)
	}

	if receiveMigration {
		return
	}

	vmConfigFile, err := os.Create("/var/run/ch-vmm/vm-config.json")

	if err != nil {
		log.Fatalf("Failed to create VM config file: %s", err)
	}

	if err := json.NewEncoder(vmConfigFile).Encode(vmConfig); err != nil {
		log.Fatalf("Failed to write VM config to file: %s", err)
	}
	vmConfigFile.Close()

	log.Println("Succeeded to setup")
}

func buildChVMConfig(ctx context.Context, vm *v1beta1.VirtualMachine) (*cloudhypervisor.VmConfig, error) {

	vmConfig := cloudhypervisor.VmConfig{
		Console: &cloudhypervisor.ConsoleConfig{
			Mode: "Pty",
		},
		Serial: &cloudhypervisor.ConsoleConfig{
			Mode: "Tty",
		},
		Payload: &cloudhypervisor.PayloadConfig{
			Kernel: "/var/lib/cloud-hypervisor/hypervisor-fw",
		},
		Cpus: &cloudhypervisor.CpusConfig{
			MaxVcpus:  int(vm.Spec.Instance.CPU.Sockets * vm.Spec.Instance.CPU.CoresPerSocket * 2),
			BootVcpus: int(vm.Spec.Instance.CPU.Sockets * vm.Spec.Instance.CPU.CoresPerSocket),
			Topology: &cloudhypervisor.CpuTopology{
				Packages:       int(vm.Spec.Instance.CPU.Sockets * 2),
				DiesPerPackage: 1,
				CoresPerDie:    int(vm.Spec.Instance.CPU.CoresPerSocket),
				ThreadsPerCore: 1,
			},
		},
		Memory: &cloudhypervisor.MemoryConfig{
			Size:          vm.Spec.Instance.Memory.Size.Value(),
			HotplugMethod: "VirtioMem",
			Prefault:      false,
			Hugepages:     false,
			HotplugSize:   vm.Spec.Instance.Memory.Size.Value() * 4,
		},
		Watchdog: true,
	}

	if vm.Spec.Instance.Memory.Hugepages != nil {
		vmConfig.Memory.Hugepages = true

	}

	if runtime.GOARCH == "arm64" {
		vmConfig.Payload.Kernel = "/var/lib/cloud-hypervisor/CLOUDHV_EFI.fd"
	}

	if vm.Spec.Instance.Kernel != nil {
		vmConfig.Payload.Kernel = "/mnt/virtmanager-kernel/vmlinux"
		vmConfig.Payload.Cmdline = vm.Spec.Instance.Kernel.Cmdline
	}

	if vm.Spec.Instance.CPU.DedicatedCPUPlacement {
		cpuSet, err := cpuset.Get()
		if err != nil {
			return nil, fmt.Errorf("get CPU set: %s", err)
		}

		pcpus := cpuSet.ToSlice()
		numVCPUs := int(vm.Spec.Instance.CPU.Sockets * vm.Spec.Instance.CPU.CoresPerSocket)
		if len(pcpus) != numVCPUs {
			// TODO: report an event to object VM
			return nil, fmt.Errorf("number of pCPUs and vCPUs must match")
		}

		for i := 0; i < numVCPUs; i++ {
			vmConfig.Cpus.Affinity = append(vmConfig.Cpus.Affinity, &cloudhypervisor.CpuAffinity{
				Vcpu:     i,
				HostCpus: []int{pcpus[i]},
			})
		}
	}

	blockVolumes := map[string]bool{}
	for _, volume := range strings.Split(os.Getenv("BLOCK_VOLUMES"), ",") {
		blockVolumes[volume] = true
	}

	for _, disk := range vm.Spec.Instance.Disks {
		for _, volume := range vm.Spec.Volumes {
			if volume.Name == disk.Name {
				diskConfig := cloudhypervisor.DiskConfig{
					Id:     disk.Name,
					Direct: true,
				}
				switch {
				case volume.ContainerDisk != nil:
					diskConfig.Path = fmt.Sprintf("/mnt/%s/disk.raw", volume.Name)
				case volume.CloudInit != nil:
					diskConfig.Path = fmt.Sprintf("/mnt/%s/cloud-init.iso", volume.Name)
				case volume.ContainerRootfs != nil:
					diskConfig.Path = fmt.Sprintf("/mnt/%s/rootfs.raw", volume.Name)
				case volume.PersistentVolumeClaim != nil, volume.DataVolume != nil, volume.VirtualDisk != nil:
					if blockVolumes[volume.Name] {
						if volume.IsHotpluggable() {
							diskConfig.Path = fmt.Sprintf("/hotplug-volumes/%s", volume.Name)
						} else {
							diskConfig.Path = fmt.Sprintf("/mnt/%s", volume.Name)
						}
					} else {
						if volume.IsHotpluggable() {
							diskConfig.Path = filepath.Join("/hotplug-volumes", fmt.Sprintf("%s.img", volume.Name))
						} else {
							diskConfig.Path = filepath.Join("/mnt", volume.Name, "disk.img")
						}
					}
				default:
					return nil, fmt.Errorf("invalid source of volume %q", volume.Name)
				}

				if disk.ReadOnly != nil && *disk.ReadOnly {
					diskConfig.Readonly = true
				}
				vmConfig.Disks = append(vmConfig.Disks, &diskConfig)
				break
			}
		}
	}

	for _, pmem := range vm.Spec.Instance.Pmem {
		for _, volume := range vm.Spec.Volumes {
			if volume.Name == pmem.Name {
				pmemConfig := &cloudhypervisor.PmemConfig{
					Id: pmem.Name,
				}
				switch {
				case volume.ContainerDisk != nil:
					pmemConfig.File = fmt.Sprintf("/mnt/%s/disk.raw", volume.Name)
				case volume.CloudInit != nil:
					pmemConfig.File = fmt.Sprintf("/mnt/%s/cloud-init.iso", volume.Name)
				case volume.ContainerRootfs != nil:
					pmemConfig.File = fmt.Sprintf("/mnt/%s/rootfs.raw", volume.Name)
				case volume.PersistentVolumeClaim != nil, volume.DataVolume != nil, volume.VirtualDisk != nil:
					if blockVolumes[volume.Name] {
						if volume.IsHotpluggable() {
							pmemConfig.File = fmt.Sprintf("/hotplug-volumes/%s", volume.Name)
						} else {
							pmemConfig.File = fmt.Sprintf("/mnt/%s", volume.Name)
						}
					} else {
						if volume.IsHotpluggable() {
							pmemConfig.File = filepath.Join("/hotplug-volumes", fmt.Sprintf("%s.img", volume.Name))
						} else {
							pmemConfig.File = filepath.Join("/mnt", volume.Name, "disk.img")
						}
					}
				default:
					return nil, fmt.Errorf("invalid source of volume %q", volume.Name)
				}

				vmConfig.Pmem = append(vmConfig.Pmem, pmemConfig)
				break
			}
		}
	}

	//GPU Device Config for cloud-hypervisor VM config
	for _, gpu := range vm.Spec.Instance.GPUs {
		envVal := os.Getenv(gpu.ResourceEnvName)
		if envVal == "" {
			return nil, fmt.Errorf("missing env var %s for GPU %s",
				gpu.ResourceEnvName, gpu.Name)
		}

		pciAddresses := strings.Split(envVal, ",")

		for i, pciAddr := range pciAddresses {
			pciAddr = strings.TrimSpace(pciAddr)

			chDeviceConfig := cloudhypervisor.DeviceConfig{
				Id:   fmt.Sprintf("%s-%d", gpu.Name, i),
				Path: fmt.Sprintf("/sys/bus/pci/devices/%s", pciAddr),
			}

			vmConfig.Devices = append(vmConfig.Devices, &chDeviceConfig)
		}
	}

	for _, fs := range vm.Spec.Instance.FileSystems {
		vmConfig.Memory.Shared = true

		if err := os.MkdirAll("/var/run/ch-vmm/virtiofsd", 0755); err != nil {

			return nil, fmt.Errorf("create virtiofsd socket dir: %s", err)
		}

		for _, volume := range vm.Spec.Volumes {
			if volume.Name == fs.Name {

				socketPath := fmt.Sprintf("/var/run/ch-vmm/virtiofsd/%s.sock", volume.Name)

				if err := exec.Command("/usr/lib/qemu/virtiofsd", "--socket-path="+socketPath, "-o", "source=/mnt/"+volume.Name, "-o", "sandbox=chroot").Start(); err != nil {
					return nil, fmt.Errorf("start virtiofsd: %s", err)
				}

				fsConfig := cloudhypervisor.FsConfig{
					Id:        fs.Name,
					Socket:    socketPath,
					Tag:       fs.Name,
					NumQueues: 1,
					QueueSize: 1024,
				}
				vmConfig.Fs = append(vmConfig.Fs, &fsConfig)
				break
			}
		}
	}

	networkStatusList := []netv1.NetworkStatus{}
	if os.Getenv("NETWORK_STATUS") != "" {
		if err := json.Unmarshal([]byte(os.Getenv("NETWORK_STATUS")), &networkStatusList); err != nil {
			return nil, err
		}
	}

	for _, iface := range vm.Spec.Instance.Interfaces {
		for networkIndex, network := range vm.Spec.Networks {
			if network.Name != iface.Name {
				continue
			}

			var linkName string
			switch {
			case network.Pod != nil:
				linkName = "eth0"
			case network.Multus != nil:
				linkName = fmt.Sprintf("net%d", networkIndex)
			default:
				return nil, fmt.Errorf("invalid source of network %q", network.Name)
			}

			switch {
			case iface.Bridge != nil:
				netConfig := cloudhypervisor.NetConfig{
					Id: iface.Name,
				}
				if err := setupBridgeNetwork(linkName, fmt.Sprintf("169.254.%d.1/30", 200+networkIndex), &netConfig); err != nil {
					return nil, fmt.Errorf("setup bridge network: %s", err)
				}
				vmConfig.Net = append(vmConfig.Net, &netConfig)
			case iface.Masquerade != nil:
				netConfig := cloudhypervisor.NetConfig{
					Id:  iface.Name,
					Mac: iface.MAC,
				}
				if err := setupMasqueradeNetwork(linkName, iface.Masquerade.CIDR, &netConfig); err != nil {
					return nil, fmt.Errorf("setup masquerade network: %s", err)
				}
				vmConfig.Net = append(vmConfig.Net, &netConfig)
			case iface.SRIOV != nil:
				for _, networkStatus := range networkStatusList {
					if networkStatus.Interface == linkName && networkStatus.DeviceInfo != nil && networkStatus.DeviceInfo.Pci != nil {
						sriovDeviceConfig := cloudhypervisor.DeviceConfig{
							Id:   iface.Name,
							Path: fmt.Sprintf("/sys/bus/pci/devices/%s", networkStatus.DeviceInfo.Pci.PciAddress),
						}
						vmConfig.Devices = append(vmConfig.Devices, &sriovDeviceConfig)
					}
				}
			case iface.VDPA != nil:
				for _, networkStatus := range networkStatusList {
					if networkStatus.Interface == linkName && networkStatus.DeviceInfo != nil && networkStatus.DeviceInfo.Vdpa != nil {
						vdpaDeviceConfig := cloudhypervisor.VdpaConfig{
							Id:        iface.Name,
							NumQueues: iface.VDPA.NumQueues,
							Iommu:     iface.VDPA.IOMMU,
							Path:      networkStatus.DeviceInfo.Vdpa.Path,
						}
						vmConfig.Vdpa = append(vmConfig.Vdpa, &vdpaDeviceConfig)
					}
				}
			case iface.VhostUser != nil:
				netConfig := cloudhypervisor.NetConfig{
					Id:        iface.Name,
					Mac:       iface.MAC,
					VhostUser: true,
					VhostMode: "Server",
				}
				if err := setupVhostUserNetwork(linkName, &netConfig); err != nil {
					return nil, fmt.Errorf("setup vhost-user network: %s", err)
				}
				vmConfig.Net = append(vmConfig.Net, &netConfig)
				vmConfig.Memory.Shared = true
			}
		}
	}
	vmConfig.Vsock = &cloudhypervisor.VsockConfig{Cid: AGENT_CID, Socket: AGENT_VSOCK}

	return &vmConfig, nil
}

func setupBridgeNetwork(linkName string, cidr string, netConfig *cloudhypervisor.NetConfig) error {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse CIDR: %s", err)
	}

	bridgeIP, err := nextIP(subnet.IP, subnet)
	if err != nil {
		return fmt.Errorf("generate bridge IP: %s", err)
	}
	bridgeIPNet := net.IPNet{
		IP:   bridgeIP,
		Mask: subnet.Mask,
	}

	link, err := netlink.LinkByName(linkName) //pod interface name eth0
	if err != nil {
		return fmt.Errorf("get link: %s", err)
	}
	netConfig.Mtu = link.Attrs().MTU

	bridgeName := fmt.Sprintf("br-%s", linkName) // create bridge in the  name of br-eh0
	bridge, err := createBridge(bridgeName, &bridgeIPNet, link.Attrs().MTU)
	if err != nil {
		return fmt.Errorf("create bridge: %s", err)
	}

	linkMAC := link.Attrs().HardwareAddr //mac address of eth0
	netConfig.Mac = linkMAC.String()

	var linkAddr *net.IPNet
	linkAddrs, err := netlink.AddrList(link, netlink.FAMILY_V4) //get all ip address of eth0
	if err != nil {
		return fmt.Errorf("list link addrs: %s", err)
	}
	if len(linkAddrs) > 0 {
		linkAddr = linkAddrs[0].IPNet //if there are  some choose first one
	}

	linkRoutes, err := netlink.RouteList(link, netlink.FAMILY_V4) // Get all the routes of eth0
	if err != nil {
		return fmt.Errorf("list link routes: %s", err)
	}

	if err := netlink.LinkSetDown(link); err != nil { //set the eth0 down
		return fmt.Errorf("down link: %s", err)
	}

	if _, err := libmacouflage.SpoofMacSameVendor(linkName, false); err != nil { //chnage mac address of eth0
		return fmt.Errorf("spoof link MAC: %s", err)
	}

	newLinkName := link.Attrs().Name
	if linkAddr != nil {
		if err := netlink.AddrDel(link, &linkAddrs[0]); err != nil { //remove the IP address of eth0 (which we have a copy of)
			return fmt.Errorf("delete link address: %s", err)
		}

		originalLinkName := link.Attrs().Name
		newLinkName = fmt.Sprintf("%s-nic", originalLinkName)

		// changing the old link same with -nic suffix
		if err := netlink.LinkSetName(link, newLinkName); err != nil { // chnage name of eth0 to eth0-nic
			return fmt.Errorf("rename link: %s", err)
		}

		dummy := &netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{
				Name: originalLinkName,
			},
		}
		if err := netlink.LinkAdd(dummy); err != nil { //create dummy interface with eth0
			return fmt.Errorf("add dummy interface: %s", err)
		}
		if err := netlink.AddrReplace(dummy, &linkAddrs[0]); err != nil { //now assign the eth0 IP address to this dummy
			return fmt.Errorf("replace dummy interface address: %s", err)
		}
	}

	if err := netlink.LinkSetMaster(link, bridge); err != nil { //now set the bridge as master of eth0-nic
		return fmt.Errorf("add link to bridge: %s", err)
	}

	if err := netlink.LinkSetUp(link); err != nil { //up the eth0-nic (this is basically eth0 with change of mac, removal of IP)
		return fmt.Errorf("up link: %s", err)
	}

	if _, err := executeCommand("bridge", "link", "set", "dev", newLinkName, "learning", "off"); err != nil { //ask this new link to not learn mac of other devices
		return fmt.Errorf("disable port MAC learning on bridge: %s", err)
	}

	tapName := fmt.Sprintf("tap-%s", linkName)
	if _, err := createTap(bridge, tapName, link.Attrs().MTU); err != nil { //create a new tap device and set bridge as the master
		return fmt.Errorf("create tap: %s", err)
	}
	netConfig.Tap = tapName

	if linkAddr != nil {
		var linkGateway net.IP
		var routes []netlink.Route
		for _, route := range linkRoutes {
			if route.Dst == nil && len(route.Src) == 0 && len(route.Gw) == 0 {
				continue
			}
			if len(linkGateway) == 0 && route.Dst == nil {
				linkGateway = route.Gw
			}
			routes = append(routes, route)
		}
		if err := startDHCPServer(bridgeName, linkMAC, linkAddr, linkGateway, routes); err != nil {
			return fmt.Errorf("start DHCP server: %s", err)
		}
	}
	return nil
}

func setupMasqueradeNetwork(linkName string, cidr string, netConfig *cloudhypervisor.NetConfig) error {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse CIDR: %s", err)
	}

	bridgeIP, err := nextIP(subnet.IP, subnet)
	if err != nil {
		return fmt.Errorf("generate bridge IP: %s", err)
	}
	bridgeIPNet := net.IPNet{
		IP:   bridgeIP,
		Mask: subnet.Mask,
	}

	link, err := netlink.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("get link: %s", err)
	}
	netConfig.Mtu = link.Attrs().MTU

	bridgeName := fmt.Sprintf("br-%s", linkName)
	bridge, err := createBridge(bridgeName, &bridgeIPNet, link.Attrs().MTU)
	if err != nil {
		return fmt.Errorf("create bridge: %s", err)
	}

	vmIP, err := nextIP(bridgeIP, subnet)
	if err != nil {
		return fmt.Errorf("generate vm IP: %s", err)
	}
	vmIPNet := &net.IPNet{
		IP:   vmIP,
		Mask: subnet.Mask,
	}

	if _, err := executeCommand("iptables", "-t", "nat", "-A", "POSTROUTING", "-o", linkName, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("add masquerade rule: %s", err)
	}
	if _, err := executeCommand("iptables", "-t", "nat", "-A", "PREROUTING", "-i", linkName, "-j", "DNAT", "--to-destination", vmIP.String()); err != nil {
		return fmt.Errorf("add prerouting rule: %s", err)
	}

	tapName := fmt.Sprintf("tap-%s", linkName)
	if _, err := createTap(bridge, tapName, link.Attrs().MTU); err != nil {
		return fmt.Errorf("create tap: %s", err)
	}
	netConfig.Tap = tapName

	vmMAC, err := net.ParseMAC(netConfig.Mac)
	if err != nil {
		return fmt.Errorf("parse VM MAC: %s", err)
	}

	if err := startDHCPServer(bridgeName, vmMAC, vmIPNet, bridgeIP, nil); err != nil {
		return fmt.Errorf("start DHCP server: %s", err)
	}
	return nil
}

func nextIP(ip net.IP, subnet *net.IPNet) (net.IP, error) {
	nextIP := make(net.IP, len(ip))
	copy(nextIP, ip)
	for j := len(nextIP) - 1; j >= 0; j-- {
		nextIP[j]++
		if nextIP[j] > 0 {
			break
		}
	}
	if subnet != nil && !subnet.Contains(nextIP) {
		return nil, fmt.Errorf("no more available IP in subnet %q", subnet.String())
	}
	return nextIP, nil
}

func createBridge(bridgeName string, bridgeIPNet *net.IPNet, mtu int) (netlink.Link, error) {
	bridge := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: bridgeName,
			MTU:  mtu,
		},
	}
	if err := netlink.LinkAdd(bridge); err != nil {
		return nil, err
	}

	if err := netlink.AddrAdd(bridge, &netlink.Addr{IPNet: bridgeIPNet}); err != nil {
		return nil, fmt.Errorf("set bridge addr: %s", err)
	}

	if err := netlink.LinkSetUp(bridge); err != nil {
		return nil, fmt.Errorf("up bridge: %s", err)
	}
	return bridge, nil
}

func createTap(bridge netlink.Link, tapName string, mtu int) (netlink.Link, error) {
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name: tapName,
			MTU:  mtu,
		},
		Mode:  netlink.TUNTAP_MODE_TAP,
		Flags: netlink.TUNTAP_DEFAULTS,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return nil, err
	}

	if err := netlink.LinkSetMaster(tap, bridge); err != nil {
		return nil, fmt.Errorf("add tap to bridge: %s", err)
	}

	if err := netlink.LinkSetUp(tap); err != nil {
		return nil, fmt.Errorf("up tap: %s", err)
	}

	createdTap, err := netlink.LinkByName(tapName)
	if err != nil {
		return nil, fmt.Errorf("get tap: %s", err)
	}
	return createdTap, nil
}

//go:embed dnsmasq.conf
var dnsmasqConf string

func startDHCPServer(ifaceName string, mac net.HardwareAddr, ipNet *net.IPNet, gateway net.IP, routes []netlink.Route) error {
	rc, err := resolvconf.Get()
	if err != nil {
		return fmt.Errorf("get resolvconf: %s", err)
	}

	dnsmasqPIDPath := fmt.Sprintf("/var/run/ch-vmm/dnsmasq/%s.pid", ifaceName)

	if err := os.MkdirAll(filepath.Dir(dnsmasqPIDPath), 0755); err != nil {
		return fmt.Errorf("create dnsmasq PID dir: %s", err)
	}

	dnsmasqConfPath := fmt.Sprintf("/var/run/ch-vmm/dnsmasq/%s.conf", ifaceName)

	if err := os.MkdirAll(filepath.Dir(dnsmasqConfPath), 0755); err != nil {
		return fmt.Errorf("create dnsmasq config dir: %s", err)
	}

	dnsmasqConfFile, err := os.Create(dnsmasqConfPath)
	if err != nil {
		return fmt.Errorf("create dnsmasq config file: %s", err)
	}
	defer dnsmasqConfFile.Close()

	data := map[string]string{
		"iface":        ifaceName,
		"mac":          mac.String(),
		"ip":           ipNet.IP.String(),
		"mask":         net.IP(ipNet.Mask).String(),
		"routes":       sortAndFormatRoutes(routes),
		"dnsServer":    strings.Join(resolvconf.GetNameservers(rc.Content, types.IPv4), ","),
		"domainSearch": strings.Join(resolvconf.GetSearchDomains(rc.Content), ","),
	}

	if len(gateway) > 0 {
		data["gateway"] = gateway.String()
	}

	if err := template.Must(template.New("dnsmasq.conf").Parse(dnsmasqConf)).Execute(dnsmasqConfFile, data); err != nil {
		return fmt.Errorf("write dnsmasq config file: %s", err)
	}

	if _, err := executeCommand("dnsmasq", fmt.Sprintf("--conf-file=%s", dnsmasqConfPath), fmt.Sprintf("--pid-file=%s", dnsmasqPIDPath)); err != nil {
		return fmt.Errorf("start dnsmasq: %s", err)
	}
	return nil
}

func sortAndFormatRoutes(routes []netlink.Route) string {
	var sortedRoutes []netlink.Route
	var defaultRoutes []netlink.Route
	for _, route := range routes {
		if route.Dst == nil {
			defaultRoutes = append(defaultRoutes, route)
			continue
		}
		sortedRoutes = append(sortedRoutes, route)
	}
	sortedRoutes = append(sortedRoutes, defaultRoutes...)

	items := []string{}
	for _, route := range sortedRoutes {
		if len(route.Gw) == 0 {
			route.Gw = net.IPv4(0, 0, 0, 0)
		}
		if route.Dst == nil {
			route.Dst = &net.IPNet{
				IP:   net.IPv4(0, 0, 0, 0),
				Mask: net.CIDRMask(0, 32),
			}
		}
		items = append(items, route.Dst.String(), route.Gw.String())
	}
	return strings.Join(items, ",")
}

func setupVhostUserNetwork(linkName string, netConfig *cloudhypervisor.NetConfig) error {
	netType := os.Getenv("NET_TYPE")
	if netType == "" {
		return fmt.Errorf("network type not found")
	}

	var socketPath string
	mtu := 1500
	switch netType {
	case "kube-ovn":
		socketPath := os.Getenv("VHOST_USER_SOCKET")
		if socketPath == "" {
			return fmt.Errorf("vhost-user socket path not found")
		}
		link, err := netlink.LinkByName("eth0")
		if err != nil {
			return fmt.Errorf("get link: %s", err)
		}
		mtu = link.Attrs().MTU
	case "userspace":
		userspaceConfigData := os.Getenv("USERSPACE_CONFIGURATION_DATA")
		if userspaceConfigData == "" {
			return fmt.Errorf("userspace configuration data not found")
		}
		var configData []userspacecni.ConfigurationData
		if err := json.Unmarshal([]byte(userspaceConfigData), &configData); err != nil {
			return fmt.Errorf("unmarshal userspace configuration data: %s", err)
		}
		var containerID string
		var socketFile string
		for _, config := range configData {
			if config.IfName == linkName {
				containerID = config.ContainerId
				socketFile = config.Config.VhostConf.Socketfile
				break
			}
		}
		if containerID == "" || socketFile == "" {
			return fmt.Errorf("vhost-user link not found")
		}
		socketDir := fmt.Sprintf("/var/run/vhost-user/%s", containerID[0:12])
		if _, err := os.Stat(socketDir); err != nil {
			if os.IsNotExist(err) {
				socketPath = fmt.Sprintf("/var/run/vhost-user/%s", socketFile)
			} else {
				return err
			}
		} else {
			socketPath = fmt.Sprintf("%s/%s", socketDir, socketFile)
		}
	}

	netConfig.Mtu = mtu
	netConfig.VhostSocket = socketPath
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

func getMemorySnapshot(vm *v1beta1.VirtualMachine, vol v1beta1.Volume) error {
	//now fetch the snapshot

	bucket := vol.MemorySnapshot.Bucket
	memPath := fmt.Sprintf("/mnt/memory/snap")
	mountPath := "/mnt/memory"
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return !os.IsNotExist(err)
	}
	if exists(memPath) {
		return nil
	}
	var err error
	if bucket != "" && strings.HasPrefix(bucket, "s3") {
		bucket = strings.TrimPrefix(bucket, "s3://")
		err = cloudutils.DownloadObjectFromS3(bucket, vol.MemorySnapshot.Key, filepath.Join(mountPath, vol.MemorySnapshot.Key))

	} else if bucket != "" && strings.HasPrefix(bucket, "gcs") {
		bucket = strings.TrimPrefix(bucket, "gcs://")
		err = cloudutils.DownloadObjectFromGCS(bucket, vol.MemorySnapshot.Key, filepath.Join(mountPath, vol.MemorySnapshot.Key))
	} else {
		err = errors.New(fmt.Sprintf("invalid Bucket name Format %s", bucket))
	}

	if err != nil {
		return err
	}

	//now decompress the file and untar it i guess.
	return volumeutil.ExtractArchive(memPath, filepath.Join(mountPath, vol.MemorySnapshot.Key))

}
