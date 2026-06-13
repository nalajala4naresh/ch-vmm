package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
)

type TLSConfig struct {
	ServerName string `mapstructure:"server_name" yaml:"server_name"`

	KeyFile string `mapstructure:"key_file" yaml:"key_file"`

	CertFile string `mapstructure:"cert_file" yaml:"cert_file"`

	CAFile string `mapstructure:"ca_file" yaml:"ca_file"`
}

type SSLCertificate struct {
	Certificate tls.Certificate
	Pool        *x509.CertPool
}

func LoadCertificate(con *TLSConfig) (*SSLCertificate, error) {

	key := strings.TrimSpace(con.KeyFile)
	ca := strings.TrimSpace(con.CAFile)
	cert := strings.TrimSpace(con.CertFile)

	if con.ServerName == "" {
		return nil, errors.New("ServerName is empty in TLSConfig")
	}
	sslcert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err

	}

	caBytes, err := os.ReadFile(ca)
	if err != nil {
		return nil, fmt.Errorf("failed to read ca certificate %s: %v", ca, err)
	}
	pool := x509.NewCertPool()
	ok := pool.AppendCertsFromPEM(caBytes)
	if !ok {
		return nil, errors.New("bad ca certificate")
	}

	return &SSLCertificate{Certificate: sslcert, Pool: pool}, nil

}
