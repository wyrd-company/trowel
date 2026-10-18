/*
---
relationships:
  references: [types, defaults, validate, expand, display]
---
*/

// Package config loads and validates the relay YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes YAML, expands environment references in values, applies
// defaults, and validates.
func Parse(raw []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var cfg Config
	if doc.Kind != 0 {
		written, err := expandNode(&doc)
		if err != nil {
			return nil, err
		}
		if err := decodeStrict(&doc, &cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		cfg.setTemplates(&doc, written)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeStrict decodes a node into v, rejecting unknown fields.
func decodeStrict(n *yaml.Node, v any) error {
	out, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
