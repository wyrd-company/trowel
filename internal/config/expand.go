package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandNode replaces every ${NAME} reference inside scalar values of the
// YAML document with the environment variable's value. Comments and keys
// are left alone. A reference to an unset variable is an error so that a
// missing secret fails at startup instead of at delivery time. It returns
// the text as written of every scalar it changed.
func expandNode(root *yaml.Node) (map[*yaml.Node]string, error) {
	missing := map[string]bool{}
	written := map[*yaml.Node]string{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.ScalarNode:
			if envRef.MatchString(n.Value) {
				written[n] = n.Value
			}
			n.Value = envRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
				name := envRef.FindStringSubmatch(ref)[1]
				val, ok := os.LookupEnv(name)
				if !ok {
					missing[name] = true
					return ref
				}
				return val
			})
		case yaml.MappingNode:
			// Values sit at odd indexes; keys are not expanded.
			for i := 1; i < len(n.Content); i += 2 {
				walk(n.Content[i])
			}
		default:
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(root)
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for k := range missing {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("config references unset environment variables: %s", strings.Join(names, ", "))
	}
	return written, nil
}
