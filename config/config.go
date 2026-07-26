package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Model struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type Provider struct {
	Name    string  `yaml:"name"`
	APIKey  string  `yaml:"api_key"`
	BaseURL string  `yaml:"base_url"`
	Models  []Model `yaml:"models"`
}

type Config struct {
	DefaultModel string     `yaml:"default_model"`
	Providers    []Provider `yaml:"providers"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Providers {
		key := cfg.Providers[i].APIKey
		if strings.HasPrefix(key, "$") {
			cfg.Providers[i].APIKey = os.Getenv(key[1:])
		}
	}
	return &cfg, nil
}
