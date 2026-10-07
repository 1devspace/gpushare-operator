// Package manifest loads ElasticModel objects from YAML files.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/reconcile"

	"gopkg.in/yaml.v3"
)

func FromDir(dir string) ([]reconcile.Item, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
			names = append(names, n)
		}
	}
	var out []reconcile.Item
	for _, n := range names {
		items, err := FromFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func FromFile(path string) ([]reconcile.Item, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []reconcile.Item
	for {
		var m v1alpha1.ElasticModel
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if m.Kind != "" && m.Kind != v1alpha1.Kind {
			continue
		}
		if m.Metadata.Name == "" || m.Spec.Model == "" {
			continue
		}
		out = append(out, reconcile.Item{Model: m, Generation: 1})
	}
	return out, nil
}
