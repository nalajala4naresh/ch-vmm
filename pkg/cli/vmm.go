package main

import (
	"context"
	"fmt"
	"os"

	config "github.com/nalajala4naresh/ch-vmm/pkg/config"
	daemon_client "github.com/nalajala4naresh/ch-vmm/pkg/daemon/proto"
	grpcutil "github.com/nalajala4naresh/ch-vmm/pkg/grpcutil"
	"github.com/spf13/cobra"
)

var cliconfig *config.Config

func init() {

	err := config.Setup("vmmconfig")
	if err != nil {
		panic(err.Error())
	}

	cliconfig, err = config.Get()
	if err != nil {
		panic(err.Error())
	}
}

func main() {

	rootCmd := &cobra.Command{Use: "vmm", Short: "vmm is cli to talk to vm's running on k8s cluster"}
	runopts := &CRunopts{}
	rootCmd.AddCommand(PauseVmCommand(runopts))
	rootCmd.AddCommand(ResumeVmCommand(runopts))
	rootCmd.AddCommand(RebootVmCommand(runopts))
	rootCmd.AddCommand(SnapshotVmCommand(runopts))
	rootCmd.AddCommand(RestoreSnapshotVmCommand(runopts))

	err := rootCmd.Execute()

	if err != nil {
		fmt.Printf("Command execution failed %s", err)
		os.Exit(1)
	}

}

func PauseVmCommand(runopts *CRunopts) *cobra.Command {

	pauseCmd := &cobra.Command{Use: "pause",
		Short: "pause  a running  guest vm",
		RunE: func(cmd *cobra.Command, args []string) error {

			// create grpc client on Unix socket to connect to the agent running inside guest
			con, err := grpcutil.Conn(cliconfig.TLSConfig.ServerName, cliconfig.TLSConfig)
			if err != nil {
				return err
			}
			client := daemon_client.NewVmServiceClient(con)
			_, err = client.PauseVM(context.Background(), &daemon_client.PauseVMRequest{Name: runopts.VMname, Namespace: runopts.VMNamespace})

			return err
		},
	}

	pauseCmd.PersistentFlags().StringVar(&runopts.VMname, "vm_name", "", "name of the vm")
	pauseCmd.PersistentFlags().StringVar(&runopts.VMNamespace, "namespace", "", "namespace the vm is supposed to run in ")
	return pauseCmd

}

func ResumeVmCommand(runopts *CRunopts) *cobra.Command {

	resumeCmd := &cobra.Command{Use: "resume",
		Short: "resume  a paused  guest vm",
		RunE: func(cmd *cobra.Command, args []string) error {

			// create grpc client on Unix socket to connect to the agent running inside guest
			con, err := grpcutil.Conn(cliconfig.TLSConfig.ServerName, cliconfig.TLSConfig)
			if err != nil {
				return err
			}
			client := daemon_client.NewVmServiceClient(con)
			_, err = client.ResumeVM(context.Background(), &daemon_client.ResumeVMRequest{Name: runopts.VMname, Namespace: runopts.VMNamespace})

			return err
		},
	}

	resumeCmd.PersistentFlags().StringVar(&runopts.VMname, "vm_name", "", "name of the vm")
	resumeCmd.PersistentFlags().StringVar(&runopts.VMNamespace, "namespace", "", "namespace the vm is supposed to run in ")
	return resumeCmd

}

func RebootVmCommand(runopts *CRunopts) *cobra.Command {

	rebootCmd := &cobra.Command{Use: "reboot",
		Short: "reboot a running vm",
		RunE: func(cmd *cobra.Command, args []string) error {

			// create grpc client on Unix socket to connect to the agent running inside guest
			con, err := grpcutil.Conn(cliconfig.TLSConfig.ServerName, cliconfig.TLSConfig)
			if err != nil {
				return err
			}
			client := daemon_client.NewVmServiceClient(con)
			_, err = client.RebootVM(context.Background(), &daemon_client.RebootVMRequest{Name: runopts.VMname, Namespace: runopts.VMNamespace})

			return err
		},
	}

	rebootCmd.PersistentFlags().StringVar(&runopts.VMname, "vm_name", "", "name of the vm")
	rebootCmd.PersistentFlags().StringVar(&runopts.VMNamespace, "namespace", "", "namespace the vm is supposed to run in ")
	return rebootCmd

}

func SnapshotVmCommand(runopts *CRunopts) *cobra.Command {

	SnapshotCmd := &cobra.Command{Use: "snapshot",
		Short: "snapshot the paused the vm",
		RunE: func(cmd *cobra.Command, args []string) error {

			// create grpc client on Unix socket to connect to the agent running inside guest
			con, err := grpcutil.Conn(cliconfig.TLSConfig.ServerName, cliconfig.TLSConfig)
			if err != nil {
				return err
			}
			client := daemon_client.NewVmServiceClient(con)
			_, err = client.CreateSnapshot(context.Background(), &daemon_client.CreateVMSnapshot{
				Name:        runopts.VMname,
				Namespace:   runopts.VMNamespace,
				Id:          runopts.SnapShotId,
				Destination: runopts.SnapShotDestination})

			return err
		},
	}
	SnapshotCmd.PersistentFlags().StringVar(&runopts.SnapShotId, "snapshot_id", "", "snapshot id ")
	SnapshotCmd.PersistentFlags().StringVar(&runopts.SnapShotDestination, "snapshot_destination", "", "snapshot destination (only directory is supported) ")
	SnapshotCmd.PersistentFlags().StringVar(&runopts.VMname, "vm_name", "", "name of the vm")
	SnapshotCmd.PersistentFlags().StringVar(&runopts.VMNamespace, "namespace", "", "namespace the vm is supposed to run in ")
	return SnapshotCmd

}

func RestoreSnapshotVmCommand(runopts *CRunopts) *cobra.Command {

	restoreSnapshotCmd := &cobra.Command{Use: "restore",
		Short: "restore  the snapshot of the vm",
		RunE: func(cmd *cobra.Command, args []string) error {

			// create grpc client on Unix socket to connect to the agent running inside guest
			con, err := grpcutil.Conn(cliconfig.TLSConfig.ServerName, cliconfig.TLSConfig)
			if err != nil {
				return err
			}
			client := daemon_client.NewVmServiceClient(con)
			_, err = client.RestoreSnapshot(context.Background(), &daemon_client.RestoreVMSnapshot{
				Name:        runopts.VMname,
				Namespace:   runopts.VMNamespace,
				Id:          runopts.SnapShotId,
				Destination: runopts.SnapShotDestination})

			return err
		},
	}
	restoreSnapshotCmd.PersistentFlags().StringVar(&runopts.SnapShotId, "snapshot_id", "", "snapshot id ")
	restoreSnapshotCmd.PersistentFlags().StringVar(&runopts.SnapShotDestination, "snapshot_destination", "", "snapshot destination (only directory is supported) ")
	restoreSnapshotCmd.PersistentFlags().StringVar(&runopts.VMname, "vm_name", "", "name of the vm")
	restoreSnapshotCmd.PersistentFlags().StringVar(&runopts.VMNamespace, "namespace", "", "namespace the vm is supposed to run in ")
	return restoreSnapshotCmd

}
