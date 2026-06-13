package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	mdvsock "github.com/mdlayher/vsock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/stats"
)

type Server struct {
	GrpcPort   int
	GRPCServer *grpc.Server
	Listener   net.Listener
	TLSConfig  *TLSConfig
	Context    context.Context
	Options    []grpc.ServerOption
}

type ServerOption func(s *Server) error

func WithGrpcPort(port int) ServerOption {

	return func(s *Server) error {
		s.GrpcPort = port
		if port > 65555 || port <= 0 {
			return fmt.Errorf("invalid port for the gprc port %d", port)

		}
		return nil
	}
}

func WithTLSConfig(config *TLSConfig) ServerOption {
	return func(s *Server) error {

		sslcert, err := LoadCertificate(config)
		if err != nil {
			return err
		}
		tlsConfig := tls.Config{
			Certificates: []tls.Certificate{sslcert.Certificate},
			ClientAuth:   tls.NoClientCert,
			RootCAs:      sslcert.Pool,
			ServerName:   config.ServerName,
		}
		fmt.Printf("servername %s", tlsConfig.ServerName)

		tc := credentials.NewTLS(&tlsConfig)

		gserverOption := grpc.Creds(tc)
		s.Options = append(s.Options, gserverOption)
		return nil

	}

}

func WithContext(c context.Context) ServerOption {
	return func(s *Server) error {
		s.Context = c
		return nil
	}
}

func WithStreamInterceptor(interceptors ...grpc.StreamServerInterceptor) ServerOption {
	return func(s *Server) error {
		if len(interceptors) > 0 {
			s.Options = append(s.Options, grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(interceptors...)))
		}

		return nil
	}
}

func WithUnaryInterceptor(interceptors ...grpc.UnaryServerInterceptor) ServerOption {
	return func(s *Server) error {
		if len(interceptors) > 0 {
			s.Options = append(s.Options, grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(interceptors...)))
		}

		return nil
	}
}

func WithStatsHandler(handler stats.Handler) ServerOption {
	return func(s *Server) error {
		if handler != nil {
			s.Options = append(s.Options, grpc.StatsHandler(handler))
		}
		return nil
	}
}

func NewServer(options ...ServerOption) (*Server, error) {

	server := &Server{Context: context.Background()}
	for _, op := range options {
		if err := op(server); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", server.GrpcPort))
	if err != nil {
		return nil, fmt.Errorf("failed to get grpc port: %d %v", server.GrpcPort, err)
	}

	// create grpc server
	server.GrpcPort = listener.Addr().(*net.TCPAddr).Port
	server.Options = append(server.Options, WithDefaultInterceptors()...)
	server.Listener = listener

	gserver := grpc.NewServer(server.Options...)

	server.GRPCServer = gserver

	//regresiter with reflection
	reflection.Register(gserver)

	return server, nil

}

func NewVsockServer(options ...ServerOption) (*Server, error) {

	server := &Server{Context: context.Background()}
	for _, op := range options {
		if err := op(server); err != nil {
			return nil, err
		}
	}
	listener, err := mdvsock.Listen(12345, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to get grpc port: %d %v", server.GrpcPort, err)
	}

	// create grpc server
	server.GrpcPort = 12345
	server.Options = append(server.Options, WithDefaultInterceptors()...)
	server.Listener = listener

	gserver := grpc.NewServer(server.Options...)

	server.GRPCServer = gserver

	//regresiter with reflection
	reflection.Register(gserver)

	return server, nil

}

func (s *Server) Run() error {
	if err := s.GRPCServer.Serve(s.Listener); err != nil {
		return fmt.Errorf("failed to start gRPC server on %d: %v", s.GrpcPort, err)
	}

	return nil
}

func (s *Server) Shutdown() error {
	s.GRPCServer.Stop()

	return nil
}
