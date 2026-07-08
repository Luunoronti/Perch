// Package config loads and saves the JSON config files for the perch
// server and client. See remote-pwsh-terminal-spec.md §5.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ServerConfig is the server-side configuration.
type ServerConfig struct {
	Listen    string   `json:"listen"`
	Shell     string   `json:"shell"`
	ShellArgs []string `json:"shell_args"`
}

// ClientConfig is the client-side configuration.
type ClientConfig struct {
	Server string `json:"server"`
}

func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Listen:    "0.0.0.0:2222",
		Shell:     defaultShellPath(),
		ShellArgs: []string{"-NoLogo"},
	}
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Server: "127.0.0.1:2222",
	}
}

// Dir returns the perch config directory, creating it if it does not exist.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "perch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LoadServerConfig loads the server config from path, creating a default
// one if it does not exist yet.
func LoadServerConfig(path string) (ServerConfig, error) {
	cfg := DefaultServerConfig()
	if err := loadJSON(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			return cfg, saveJSON(path, cfg)
		}
		return cfg, err
	}
	return cfg, nil
}

// LoadClientConfig loads the client config from path, creating a default
// one if it does not exist yet.
func LoadClientConfig(path string) (ClientConfig, error) {
	cfg := DefaultClientConfig()
	if err := loadJSON(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			return cfg, saveJSON(path, cfg)
		}
		return cfg, err
	}
	return cfg, nil
}

func loadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func saveJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
