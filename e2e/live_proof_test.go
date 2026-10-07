// Live proof: ElasticModel → GPU profile → operator mode → completion.
//
// Run: LLC_LIVE_PROOF=1 go test ./e2e -run TestLiveOperatorProof -count=1 -timeout 20m
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/cluster"
	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/operator"
	"gpushare-elastic/pkg/profile"
	"gpushare-elastic/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const livePort = "8091"

func gpuShareRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(repoRoot(t), "..", "GPUShare-LLM")
	if _, err := os.Stat(filepath.Join(root, "poc", "bin", "profile_job.py")); err != nil {
		t.Fatalf("GPUShare-LLM not next to this repo: %v", err)
	}
	return root
}

func TestLiveOperatorProof(t *testing.T) {
	if os.Getenv("LLC_LIVE_PROOF") != "1" {
		t.Skip("set LLC_LIVE_PROOF=1 to run GPU + operator proof")
	}

	cfg := startAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := cluster.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: crdNS}}
	if _, err := core.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = core.CoreV1().Namespaces().Delete(context.Background(), crdNS, metav1.DeleteOptions{})
	})

	obj := em("qwen-proof", qwen, "1400Mi", 10, "80ms", 1, 512)
	_ = cli.Delete(ctx, crdNS, obj.Metadata.Name)
	if err := cli.Create(ctx, obj); err != nil {
		t.Fatalf("create ElasticModel: %v", err)
	}
	t.Log("1. user object stored (no mode in spec)")

	items := mustItem(t, cli, ctx, obj.Metadata.Name)
	if items[0].Model.Spec.Model != qwen || items[0].Model.Spec.MemoryBudget != "1400Mi" {
		t.Fatalf("spec not stored: %+v", items[0].Model.Spec)
	}

	opt := operator.Options{
		Namespace: crdNS, DeviceBudgetMB: 1200, CreateJobs: true,
		Profiler: profiler.Spec{Stub: true, GPUDevices: false},
	}
	rep, err := operator.Run(ctx, cli, items, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.PatchStatuses(ctx, crdNS, rep); err != nil {
		t.Fatal(err)
	}
	if _, err := core.BatchV1().Jobs(crdNS).Get(ctx, profiler.JobName(qwen), metav1.GetOptions{}); err != nil {
		t.Fatalf("profiler Job missing: %v", err)
	}
	got, err := cli.Get(ctx, crdNS, obj.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Profiling" {
		t.Fatalf("want Profiling, got %s (%s)", got.Status.Phase, got.Status.Reason)
	}
	t.Logf("2. first-seen → Job %s, phase Profiling", profiler.JobName(qwen))

	doc := runGPUProfile(t, gpuShareRoot(t))
	if !doc.OK() || doc.Elastic.DriverMB >= doc.Resident.DriverMB {
		t.Fatalf("profile not usable: %+v", doc)
	}
	if err := cli.WriteProfileConfigMap(ctx, crdNS, qwen, doc); err != nil {
		t.Fatal(err)
	}
	t.Logf("3. GPU profile written: elastic=%.0f MB resident=%.0f MB source=%s",
		doc.Elastic.DriverMB, doc.Resident.DriverMB, doc.Source)

	rep, err = operator.Run(ctx, cli, items, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.PatchStatuses(ctx, crdNS, rep); err != nil {
		t.Fatal(err)
	}
	got, err = cli.Get(ctx, crdNS, obj.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Ready" || got.Status.Assigned.Mode == "" {
		t.Fatalf("mode not assigned: phase=%s mode=%s reason=%s",
			got.Status.Phase, got.Status.Assigned.Mode, got.Status.Reason)
	}
	if got.Status.Assigned.Mode != "elastic" {
		t.Fatalf("1200 MB device should pick elastic, got %s (%s)",
			got.Status.Assigned.Mode, got.Status.Reason)
	}
	t.Logf("4. operator chose %s r%d  predicted=%.0f MB",
		got.Status.Assigned.Mode, got.Status.Assigned.ResidentLayers,
		got.Status.Assigned.PredictedDriverMB)

	stop := startWorker(t, gpuShareRoot(t), got.Status.Assigned)
	defer stop()
	text := complete(t, "The capital of France is")
	if text == "" {
		t.Fatal("empty completion")
	}
	t.Logf("5. inference OK: %q", trimRunes(text, 80))
}

func mustItem(t *testing.T, cli *cluster.Client, ctx context.Context, name string) []reconcile.Item {
	t.Helper()
	items, err := cli.List(ctx, crdNS)
	if err != nil {
		t.Fatal(err)
	}
	var scoped []reconcile.Item
	for _, it := range items {
		if it.Model.Metadata.Name == name {
			scoped = append(scoped, it)
		}
	}
	if len(scoped) != 1 {
		t.Fatalf("listed %d", len(scoped))
	}
	return scoped
}

func runGPUProfile(t *testing.T, root string) profile.Doc {
	t.Helper()
	out := filepath.Join(t.TempDir(), "profile.json")
	cmd := exec.Command(pythonBin(), filepath.Join(root, "poc", "bin", "profile_job.py"),
		"--model", qwen, "--out", out, "--force")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+filepath.Join(root, "poc"),
		"PYTHONUNBUFFERED=1",
	)
	raw, err := cmd.CombinedOutput()
	t.Logf("profile_job:\n%s", raw)
	if err != nil {
		t.Fatalf("GPU profile_job failed: %v", err)
	}
	doc, err := profile.ReadFile(out)
	if err != nil || doc == nil {
		t.Fatalf("read profile: %v", err)
	}
	return *doc
}

func startWorker(t *testing.T, root string, a v1alpha1.Assigned) func() {
	t.Helper()
	log, err := os.CreateTemp("", "llc-proof-worker-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pythonBin(), filepath.Join(root, "poc", "bin", "serve.py"),
		"--model", qwen,
		"--port", livePort,
		"--mode", a.Mode,
		"--resident-layers", fmt.Sprintf("%d", a.ResidentLayers),
		"--batch", fmt.Sprintf("%d", max(a.Batch, 1)),
		"--budget-mb", "1400",
		"--request-timeout", "120",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+filepath.Join(root, "poc"),
		"PYTHONUNBUFFERED=1",
	)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(7 * time.Minute)
	for {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("worker exited; see %s", log.Name())
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("worker not ready; see %s", log.Name())
		}
		resp, err := http.Get("http://127.0.0.1:" + livePort + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(time.Second)
	}
	t.Logf("worker ready on :%s (log %s)", livePort, log.Name())
	return func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = log.Close()
	}
}

func complete(t *testing.T, prompt string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"prompt": prompt, "max_tokens": 10,
	})
	resp, err := http.Post("http://127.0.0.1:"+livePort+"/v1/completions",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("completion %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) == 0 {
		t.Fatalf("no choices: %s", raw)
	}
	return out.Choices[0].Text
}

func pythonBin() string {
	if p := os.Getenv("PYTHON"); p != "" {
		return p
	}
	return "python3"
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
