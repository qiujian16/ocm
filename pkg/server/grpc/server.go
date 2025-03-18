package grpc

import (
	"context"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	commonoptions "open-cluster-management.io/ocm/pkg/common/options"
	"open-cluster-management.io/ocm/pkg/server/services"
	"open-cluster-management.io/ocm/pkg/version"
	grpcserver "open-cluster-management.io/sdk-go/pkg/cloudevents/server/grpc"

	clusterv1client "open-cluster-management.io/api/client/cluster/clientset/versioned"
	clusterv1informers "open-cluster-management.io/api/client/cluster/informers/externalversions"
)

func NewGRPCServer() *cobra.Command {
	opts := commonoptions.NewOptions()
	grpcServerOpts := grpcServerOptions{}
	cmdConfig := opts.
		NewControllerCommandConfig("grpc-server", version.Get(), grpcServerOpts.Run)
	cmd := cmdConfig.NewCommandWithContext(context.TODO())
	cmd.Use = "grpc-server"
	cmd.Short = "Start the gRPC Server"

	flags := cmd.Flags()
	opts.AddFlags(flags)
	grpcServerOpts.AddFlags(flags)

	return cmd
}

type grpcServerOptions struct {
}

func NewGRPCServerOptions() *grpcServerOptions {
	return &grpcServerOptions{}
}

func (o *grpcServerOptions) AddFlags(flags *pflag.FlagSet) {
}

func (o *grpcServerOptions) Run(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	grpcServerOptions := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(grpcServerOptions...)
	grpcEventServer := grpcserver.NewGRPCBroker(grpcServer, ":8888")
	clusterClient, err := clusterv1client.NewForConfig(controllerContext.KubeConfig)
	if err != nil {
		return err
	}
	clusterInformers := clusterv1informers.NewSharedInformerFactory(clusterClient, 30*time.Minute)
	grpcEventServer.RegisterService(
		cluster.ManagedClusterEventDataType,
		services.NewClusterService(clusterClient, clusterInformers.Cluster().V1().ManagedClusters()))
	go clusterInformers.Start(ctx.Done())
	go grpcEventServer.Start(ctx)

	<-ctx.Done()
	return nil
}
