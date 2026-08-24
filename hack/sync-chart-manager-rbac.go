//go:build ignore

// Sync charts/nominatim-operator/templates/_manager-role-rules.tpl from
// config/rbac/role.yaml (kubebuilder SSOT). Invoked by make manifests / check-chart-rbac.
//
//	go run ./hack/sync-chart-manager-rbac.go
//	go run ./hack/sync-chart-manager-rbac.go --check
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

const (
	rolePath  = "config/rbac/role.yaml"
	chartPath = "charts/nominatim-operator/templates/_manager-role-rules.tpl"
	header    = `{{/*
Generated from config/rbac/role.yaml — do not edit by hand.
Regenerate: make manifests
*/}}
{{- define "nominatim-operator.managerRoleRules" -}}
`
	footer = `{{- end -}}
`
)

type clusterRole struct {
	Rules []map[string]any `json:"rules"`
}

func main() {
	check := flag.Bool("check", false, "exit non-zero if chart rules drift from role.yaml")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	want, err := rulesYAML(filepath.Join(root, rolePath))
	if err != nil {
		fail(err)
	}
	out := append([]byte(header), want...)
	out = append(out, []byte(footer)...)

	dest := filepath.Join(root, chartPath)
	if *check {
		got, err := os.ReadFile(dest)
		if err != nil {
			fail(fmt.Errorf("read %s: %w (run: make manifests)", chartPath, err))
		}
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(out)) {
			fail(fmt.Errorf("%s is out of sync with %s\nrun: make manifests", chartPath, rolePath))
		}
		fmt.Printf("ok: %s matches %s\n", chartPath, rolePath)
		return
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", chartPath, len(out))
}

func rulesYAML(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var role clusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(role.Rules) == 0 {
		return nil, fmt.Errorf("%s: no rules", path)
	}
	return yaml.Marshal(role.Rules)
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", wd)
		}
		dir = parent
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "sync-chart-manager-rbac: %v\n", err)
	os.Exit(1)
}
