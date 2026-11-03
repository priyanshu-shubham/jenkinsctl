// Package config persists named Jenkins server profiles.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Server struct {
	URL      string `json:"url"`
	User     string `json:"user"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

type Config struct {
	Servers map[string]*Server `json:"servers"`

	path string
}

func DefaultPath() (string, error) {
	if p := os.Getenv("JENKINSCTL_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jenkinsctl", "config.json"), nil
}

// Load returns an empty config when the file does not exist yet.
func Load(path string) (*Config, error) {
	c := &Config{Servers: map[string]*Server{}, path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	return c, nil
}

// Save writes atomically with 0600 since the file holds tokens.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
