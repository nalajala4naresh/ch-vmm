package grpc

import (
	"context"
	"strings"

	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func UnixConn(socket string, addr string, tls *TLSConfig) (*grpc.ClientConn, error) {

	dailer := func(ctx context.Context, addr string) (net.Conn, error) {
		return net.Dial("unix", socket)
	}
	tlsOpt := grpc.WithTransportCredentials(insecure.NewCredentials())
	if tls != nil {
		sslCerts, err := LoadCertificate(tls)
		if err != nil {
			return nil, err
		}
		tc := credentials.NewClientTLSFromCert(sslCerts.Pool, addr)
		tlsOpt = grpc.WithTransportCredentials(tc)

	}
	dailOpt := grpc.WithContextDialer(dailer)

	conn, err := grpc.NewClient(addr, dailOpt, tlsOpt)
	if err != nil {
		return nil, err
	}

	return conn, err

}
func Conn(url string, tls *TLSConfig, options ...grpc.DialOption) (*grpc.ClientConn, error) {

	dialOpts := grpc.WithTransportCredentials(insecure.NewCredentials())
	if tls != nil {
		sslCerts, err := LoadCertificate(tls)
		if err != nil {
			return nil, err
		}
		dialOpts = tlsDial(url, sslCerts)

	}

	return grpc.NewClient(url, dialOpts)

}

func tlsDial(servername string, ssl *SSLCertificate) grpc.DialOption {
	server := strings.Split(servername, ":")
	tc := credentials.NewClientTLSFromCert(ssl.Pool, server[0])
	return grpc.WithTransportCredentials(tc)
}
