package config

import (
	"fmt"

	grpc_util "github.com/nalajala4naresh/ch-vmm/pkg/grpcutil"
	"github.com/spf13/viper"
)

// Config contains top level configuration for frontier.
type Config struct {
	GRPCPort  int                  `mapstructure:"grpc_port" yaml:"grpc_port"`
	TLSConfig *grpc_util.TLSConfig `mapstructure:"tls_config" yaml:"tls_config"`
}

var readConf = viper.ReadInConfig

// Setup prepares to read the config. It should only be called once.
func Setup(name string) error {
	viper.SetConfigName(name)
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/config")
	viper.AddConfigPath("$HOME/.daemon")
	viper.AddConfigPath("$HOME/.vmm")

	viper.AddConfigPath(".")

	err := readConf()
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
	}

	return nil
}

var unmarshal = viper.Unmarshal

// Get returns the config read from the conf file.
func Get() (*Config, error) {
	conf := &Config{}
	err := unmarshal(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	return conf, nil
}
