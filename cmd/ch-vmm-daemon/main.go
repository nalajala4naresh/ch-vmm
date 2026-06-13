package main

import (
	"context"

	"flag"
	"fmt"
	"os"

	grpc "github.com/nalajala4naresh/ch-vmm/pkg/grpcutil"
	grpc_util "github.com/nalajala4naresh/ch-vmm/pkg/grpcutil"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nalajala4naresh/ch-vmm/pkg/config"
	"github.com/nalajala4naresh/ch-vmm/pkg/daemon"
	"github.com/nalajala4naresh/ch-vmm/pkg/daemon/deviceplugin"
	daemon_proto "github.com/nalajala4naresh/ch-vmm/pkg/daemon/proto"
	"github.com/nalajala4naresh/ch-vmm/pkg/daemon/tcpproxy"
	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(snapv1.AddToScheme(scheme))
	utilruntime.Must(v1beta1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err = (&daemon.VMReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("virt-daemon-vm"),
		NodeName:      os.Getenv("NODE_NAME"),
		NodeIP:        os.Getenv("NODE_IP"),
		RelayProvider: tcpproxy.NewRelayProvider(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VMReconciler")
		os.Exit(1)
	}

	if err = mgr.Add(deviceplugin.NewDevicePluginManager()); err != nil {
		setupLog.Error(err, "unable to create device plugin manager")
		os.Exit(1)
	}

	if err = (&daemon.VMSnapShotReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("virt-daemon-vmsnapshot"),
		NodeName: os.Getenv("NODE_NAME"),
		NodeIP:   os.Getenv("NODE_IP"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VMSnapShotReconciler")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting grpc server")

	err = config.Setup("daemon-conf")
	if err != nil {
		panic(fmt.Sprintf("unable to get config %s", err))
	}
	gconfig, err := config.Get()
	if err != nil {
		panic(fmt.Sprintf("unable to get config %s", err))
	}

	go func() {
		setupLog.Info("starting manager")
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	}()

	//add options for grpc port , tls config and other config options
	server, err := grpc_util.NewServer(grpc_util.WithContext(context.Background()),
		grpc.WithGrpcPort(gconfig.GRPCPort),
		grpc.WithTLSConfig(gconfig.TLSConfig))
	if err != nil {
		panic(fmt.Sprintf("unable to start grpc server %s", err))
	}
	vmm := daemon.NewServer(mgr.GetClient())
	daemon_proto.RegisterVmServiceServer(server.GRPCServer, vmm)
	defer server.Shutdown()
	server.Run()

}
