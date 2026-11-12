/*
---
relationships:
  references: [config, expand, display]
---
*/

package config

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SnippetDirName is the directory, beside the config file, that holds
// configuration snippets.
const SnippetDirName = "conf.d"

// Source is one file of configuration: the config file or a snippet.
type Source struct {
	// Path names the file in errors. It is empty for the config file when
	// it was not read from disk.
	Path string
	Raw  []byte
}

// SnippetDir returns the snippet directory for the config file at path.
func SnippetDir(path string) string {
	return filepath.Join(filepath.Dir(path), SnippetDirName)
}

// Read reads the config file at path, then every snippet in its snippet
// directory in lexical order of file name. A snippet is a file whose name
// ends in .yml or .yaml and does not start with a dot. A missing snippet
// directory holds no snippets.
func Read(path string) ([]Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	srcs := []Source{{Path: path, Raw: raw}}
	dir := SnippetDir(path)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return srcs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read snippets: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !isSnippetName(name) {
			continue
		}
		p := filepath.Join(dir, name)
		// Stat follows symlinks, so a link to a file is a snippet and a
		// link to a directory is not.
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("read snippets: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read snippets: %w", err)
		}
		srcs = append(srcs, Source{Path: p, Raw: raw})
	}
	return srcs, nil
}

func isSnippetName(name string) bool {
	return !strings.HasPrefix(name, ".") &&
		(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"))
}

// Digest identifies the names and contents of srcs, so a reload can skip
// configuration it has already tried.
func Digest(srcs []Source) [sha256.Size]byte {
	h := sha256.New()
	for _, s := range srcs {
		for _, b := range [][]byte{[]byte(s.Path), s.Raw} {
			_ = binary.Write(h, binary.BigEndian, uint64(len(b)))
			h.Write(b)
		}
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// mergedSections are the top-level sections whose lists every file adds
// to. Every other section may be set in only one file.
var mergedSections = map[string]bool{"webhooks": true, "subjects": true}

// ParseSources parses each source, merges them in order, and then applies
// defaults and validates as Parse does. The webhooks and subjects lists of
// every source are joined in order. Any other top-level section may be set
// in only one source. Errors in a snippet name its path.
func ParseSources(srcs []Source) (*Config, error) {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	written := map[*yaml.Node]string{}
	setIn := map[string]string{}
	lists := map[string]*yaml.Node{}
	for i, src := range srcs {
		where := func(err error) error {
			if i == 0 || src.Path == "" {
				return err
			}
			return fmt.Errorf("%s: %w", src.Path, err)
		}
		m, w, err := parseSource(src.Raw)
		if err != nil {
			return nil, where(err)
		}
		for n, t := range w {
			written[n] = t
		}
		if m == nil {
			continue
		}
		for j := 0; j+1 < len(m.Content); j += 2 {
			key, val := m.Content[j], m.Content[j+1]
			if key.Value == "<<" {
				return nil, where(errors.New("parse config: a merge key (<<) is not supported at the top level"))
			}
			if val.Kind == yaml.AliasNode {
				val = val.Alias
			}
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
				// An empty section sets nothing; decodeStrict accepted it.
				continue
			}
			if mergedSections[key.Value] {
				if l, ok := lists[key.Value]; ok {
					l.Content = append(l.Content, val.Content...)
					continue
				}
				l := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: append([]*yaml.Node(nil), val.Content...)}
				lists[key.Value] = l
				root.Content = append(root.Content, key, l)
				continue
			}
			if first, ok := setIn[key.Value]; ok {
				return nil, where(fmt.Errorf("%s is already set in %s; set it in one file only", key.Value, nameOr(first)))
			}
			setIn[key.Value] = src.Path
			root.Content = append(root.Content, key, val)
		}
	}
	var cfg Config
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	// Decode the merged nodes directly rather than through decodeStrict.
	// Marshalling would reorder anchors across files and rebind each alias
	// by name; decoding the nodes follows each alias to the node it named
	// in its own file. parseSource already rejected unknown fields.
	if err := doc.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.setTemplates(doc, written)
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// parseSource decodes one file, expands its environment references, and
// checks it decodes strictly on its own, so an error points at this file.
// It returns the top-level mapping, nil for an empty file, and the text as
// written of every scalar expansion changed.
func parseSource(raw []byte) (*yaml.Node, map[*yaml.Node]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil, nil
	}
	written, err := expandNode(&doc)
	if err != nil {
		return nil, nil, err
	}
	if err := decodeStrict(&doc, &Config{}); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		// A null document; decodeStrict rejects every other kind.
		return nil, written, nil
	}
	return m, written, nil
}

func nameOr(path string) string {
	if path == "" {
		return "the config file"
	}
	return path
}
