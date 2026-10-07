package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/shairoth12/trawl"
)

// loadConfig reads a YAML config file and returns the parsed Config.
// If path is empty, it returns a zero-value Config (no error).
func loadConfig(path string) (trawl.Config, error) {
	if path == "" {
		return trawl.Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return trawl.Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg trawl.Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return trawl.Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err = validateConfig(cfg); err != nil {
		return trawl.Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// validateConfig checks that every indicator has a non-empty Package and
// ServiceType. It returns an error describing the first invalid entry found.
func validateConfig(cfg trawl.Config) error {
	for i, ind := range cfg.Indicators {
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
