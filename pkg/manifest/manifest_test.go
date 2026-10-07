package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(`
apiVersion: inference.gpushare.io/v1alpha1
kind: ElasticModel
metadata:
  name: chat
  namespace: llc
spec:
  model: Qwen/Qwen2.5-0.5B-Instruct
  memoryBudget: 1400Mi
  ttftSLO: 80ms
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignore
---
apiVersion: inference.gpushare.io/v1alpha1
kind: ElasticModel
metadata:
  name: tiny
spec:
  model: Qwen/Qwen2.5-0.5B-Instruct
  memoryBudget: 400Mi
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	items, err := FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d", len(items))
	}
	if items[0].Model.Metadata.Name != "chat" || items[0].Model.Spec.TTFTSLO != "80ms" {
		t.Fatalf("%+v", items[0].Model)
	}
}
