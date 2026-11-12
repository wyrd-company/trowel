/*
---
relationships:
  references: [types, defaults, validate, expand, display, sources]
---
*/

// Package config loads and validates the relay YAML configuration.
package config

import (
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes YAML, expands environment references in values, applies
// defaults, and validates.
func Parse(raw []byte) (*Config, error) {
	return ParseSources([]Source{{Raw: raw}})
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
