package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	ListenAddrs     []string `json:"listen_addrs"`
	BootstrapPeers  []string `json:"bootstrap_peers"`
	PrivKeyFilePath string   `json:"private_key_file"`
	PubKeyFilePath  string   `json:"public_key_file"`
}

func DefaultConfig() Config {
	return Config{
		ListenAddrs: []string{
			"/ip4/0.0.0.0/tcp/7673",
			"/ip4/0.0.0.0/udp/7673/quic",
			"/ip6/::/tcp/6764",
			"/ip6/::/udp/6764/quic",
		},
		BootstrapPeers:  []string{},
		PrivKeyFilePath: "./bootstrap-node-privkey",
		PubKeyFilePath:  "./bootstrap-node-pubkey",
	}
}

func LoadConfig(filePath string) (Config, error) {
	cfg := DefaultConfig()
	if filePath != "" {
		cfgFile, err := os.Open(filePath)
		if err != nil {
			return Config{}, err
		}
		defer cfgFile.Close()

		decoder := json.NewDecoder(cfgFile)
		err = decoder.Decode(&cfg)
		if err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}
