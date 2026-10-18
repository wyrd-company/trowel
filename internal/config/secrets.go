/*
---
relationships:
  references: [types]
---
*/

package config

import (
	"fmt"
	"os"
	"strings"
)

func validateSecret(where, field, inline, file string, vault *VaultRef) error {
	n := 0
	for _, set := range []bool{inline != "", file != "", vault != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%s: exactly one of %s, %s_file, or %s_vault is required", where, field, field, field)
	}
	return nil
}

// SecretValue returns the inline value or the trimmed contents of the file.
func SecretValue(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
