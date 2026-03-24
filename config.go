package trawl

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadConfig reads a YAML config file and returns the parsed Config.
// If path is empty, it returns a zero-value Config (no error).
func LoadConfig(ctx context.Context, path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err = cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks that every indicator has a non-empty Package and ServiceType.
// It returns an error describing the first invalid entry found.
func (c Config) Validate() error {
	for i, ind := range c.Indicators {
		if ind.Package == "" {
			return fmt.Errorf("indicator %d: package must not be empty", i)
		}
		if ind.ServiceType == "" {
			return fmt.Errorf("indicator %d (%s): service_type must not be empty", i, ind.Package)
		}
		for j, wp := range ind.WrapperFor {
			if wp == "" {
				return fmt.Errorf("indicator %d (%s): wrapper_for[%d] must not be empty", i, ind.Package, j)
			}
		}
	}
	return nil
}
