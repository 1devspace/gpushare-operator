// End-to-end: install the CRD, load measured profiles, apply user ElasticModel
// specs, reconcile, and assert the mode written back to status.
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/cluster"
	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/operator"
	"gpushare-elastic/pkg/profile"
	"gpushare-elastic/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apiext "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	qwen  = "Qwen/Qwen2.5-0.5B-Instruct"
	smol  = "HuggingFaceTB/SmolLM2-360M-Instruct"
	crdNS = "llc-e2e"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func startAPI(t *testing.T) *rest.Config {
	t.Helper()
	root := repoRoot(t)
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		assets = filepath.Join(os.Getenv("HOME"), ".local", "envtest", "k8s", "1.31.0-linux-amd64")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "config", "crd")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assets,
	}
	cfg, err := env.Start()
	if err == nil {
		t.Cleanup(func() { _ = env.Stop() })
		return cfg
	}
	t.Logf("envtest not available (%v); trying KUBECONFIG", err)
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Skipf("no envtest assets and no KUBECONFIG: %v", err)
	}
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	return cfg
}

func TestEndToEndInstallProfileSpecAndMode(t *testing.T) {
	cfg := startAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ext, err := apiext.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := cluster.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("install_crd", func(t *testing.T) {
		deadline := time.Now().Add(20 * time.Second)
		var last error
		for time.Now().Before(deadline) {
			got, err := ext.ApiextensionsV1().CustomResourceDefinitions().Get(
				ctx, "elasticmodels.inference.gpushare.io", metav1.GetOptions{},
			)
			last = err
			if err == nil {
				for _, c := range got.Status.Conditions {
					if string(c.Type) == "Established" && string(c.Status) == "True" {
						if got.Spec.Names.Kind != v1alpha1.Kind {
							t.Fatalf("kind %s", got.Spec.Names.Kind)
						}
						return
					}
				}
				last = err
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("CRD not established: %v", last)
	})

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: crdNS}}
	if _, err := core.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = core.CoreV1().Namespaces().Delete(context.Background(), crdNS, metav1.DeleteOptions{})
	})

	root := repoRoot(t)
	profiles := reconcile.LoadProfilesFromDir(filepath.Join(root, "testdata", "profiles"), []string{qwen, smol})
	t.Run("profiling_cache", func(t *testing.T) {
		if _, ok := profiles[qwen]; !ok {
			t.Fatalf("missing profile for %s", qwen)
		}
		if _, ok := profiles[smol]; !ok {
			t.Fatalf("missing profile for %s", smol)
		}
		if profiles[qwen].Elastic.DriverMB <= 0 || profiles[qwen].Resident.DriverMB <= 0 {
			t.Fatalf("qwen profile incomplete: %+v", profiles[qwen])
		}
		if profiles[qwen].Elastic.DriverMB >= profiles[qwen].Resident.DriverMB {
			t.Fatalf("elastic must be cheaper than resident")
		}
	})

	type want struct {
		phase  string
		mode   string
		minR   int
		reason string
	}
	cases := []struct {
		name       string
		deviceMB   float64
		objects    []v1alpha1.ElasticModel
		expect     map[string]want
		deviceMode string
	}{
		{
			name:     "user_spec_too_small_is_rejected",
			deviceMB: 4096,
			objects: []v1alpha1.ElasticModel{
				em("too-small", qwen, "400Mi", 1, "", 1, 512),
			},
			expect: map[string]want{
				"too-small": {phase: "Rejected", reason: "MB"},
			},
		},
		{
			name:     "tight_budget_chooses_elastic_and_packs_layers",
			deviceMB: 1200,
			objects: []v1alpha1.ElasticModel{
				em("qwen-chat", qwen, "1400Mi", 10, "80ms", 4, 1024),
			},
			deviceMode: "elastic",
			expect: map[string]want{
				"qwen-chat": {phase: "Ready", mode: "elastic", minR: 2},
			},
		},
		{
			name:     "roomy_budget_chooses_resident",
			deviceMB: 2600,
			objects: []v1alpha1.ElasticModel{
				em("qwen-chat", qwen, "1400Mi", 10, "", 1, 512),
			},
			deviceMode: "resident",
			expect: map[string]want{
				"qwen-chat": {phase: "Ready", mode: "resident"},
			},
		},
		{
			name:     "ttft_slo_rules_out_swap",
			deviceMB: 1800,
			objects: []v1alpha1.ElasticModel{
				func() v1alpha1.ElasticModel {
					m := em("chat-slo", qwen, "1400Mi", 10, "55ms", 1, 512)
					m.Spec.Workload = v1alpha1.Workload{Concurrency: 1, ExpectedOutputTokens: 16, RequestsPerSwitch: 1}
					return m
				}(),
				em("other", smol, "800Mi", 5, "", 1, 512),
			},
			deviceMode: "elastic",
			expect: map[string]want{
				"chat-slo": {phase: "Ready", mode: "elastic"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, obj := range tc.objects {
				_ = cli.Delete(ctx, crdNS, obj.Metadata.Name)
			}
			for _, obj := range tc.objects {
				obj.Metadata.Namespace = crdNS
				if err := cli.Create(ctx, obj); err != nil {
					t.Fatalf("create %s: %v", obj.Metadata.Name, err)
				}
			}

			items, err := cli.List(ctx, crdNS)
			if err != nil {
				t.Fatal(err)
			}
			var scoped []reconcile.Item
			wantNames := map[string]bool{}
			for _, o := range tc.objects {
				wantNames[o.Metadata.Name] = true
			}
			for _, it := range items {
				if wantNames[it.Model.Metadata.Name] {
					scoped = append(scoped, it)
				}
			}
			if len(scoped) != len(tc.objects) {
				t.Fatalf("listed %d objects, want %d", len(scoped), len(tc.objects))
			}

			report := reconcile.Run(scoped, reconcile.Options{
				DeviceBudgetMB: tc.deviceMB,
				Profiles:       profiles,
			})
			if tc.deviceMode != "" {
				if report.DeviceMode == nil || !report.DeviceMode.Feasible || report.DeviceMode.Mode != tc.deviceMode {
					t.Fatalf("device mode got %+v want %s", report.DeviceMode, tc.deviceMode)
				}
			}
			if err := cli.PatchStatuses(ctx, crdNS, report); err != nil {
				t.Fatal(err)
			}

			for name, w := range tc.expect {
				got, err := cli.Get(ctx, crdNS, name)
				if err != nil {
					t.Fatalf("get %s: %v", name, err)
				}
				if got.Spec.Model == "" || got.Spec.MemoryBudget == "" {
					t.Fatalf("%s spec not stored: %+v", name, got.Spec)
				}
				if got.Status.Phase != w.phase {
					t.Fatalf("%s phase %q want %q (%s)", name, got.Status.Phase, w.phase, got.Status.Reason)
				}
				if w.mode != "" && got.Status.Assigned.Mode != w.mode {
					t.Fatalf("%s mode %q want %q", name, got.Status.Assigned.Mode, w.mode)
				}
				if w.minR > 0 && got.Status.Assigned.ResidentLayers < w.minR {
					t.Fatalf("%s residentLayers %d want >= %d", name, got.Status.Assigned.ResidentLayers, w.minR)
				}
				if w.reason != "" && !contains(got.Status.Reason, w.reason) {
					t.Fatalf("%s reason %q should contain %q", name, got.Status.Reason, w.reason)
				}
			}
		})
	}
}

func TestFirstSeenJobThenProfileThenMode(t *testing.T) {
	cfg := startAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	obj := em("qwen-first", qwen, "1400Mi", 10, "80ms", 4, 1024)
	_ = cli.Delete(ctx, crdNS, obj.Metadata.Name)
	if err := cli.Create(ctx, obj); err != nil {
		t.Fatalf("create: %v", err)
	}

	items, err := cli.List(ctx, crdNS)
	if err != nil {
		t.Fatal(err)
	}
	var scoped []reconcile.Item
	for _, it := range items {
		if it.Model.Metadata.Name == obj.Metadata.Name {
			scoped = append(scoped, it)
		}
	}

	opt := operator.Options{
		Namespace: crdNS, DeviceBudgetMB: 1200, CreateJobs: true,
		Profiler: profiler.Spec{Stub: true, GPUDevices: false},
	}
	rep, err := operator.Run(ctx, cli, scoped, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.PatchStatuses(ctx, crdNS, rep); err != nil {
		t.Fatal(err)
	}

	job, err := core.BatchV1().Jobs(crdNS).Get(ctx, profiler.JobName(qwen), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("profiler Job not created: %v", err)
	}
	if job.Labels[profiler.LabelManaged] != profiler.ManagedBy {
		t.Fatalf("Job is not an operator operand: %+v", job.Labels)
	}
	got, err := cli.Get(ctx, crdNS, obj.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Profiling" {
		t.Fatalf("phase %q want Profiling (%s)", got.Status.Phase, got.Status.Reason)
	}

	doc, err := profile.ReadFile(filepath.Join(repoRoot(t), "testdata", "profiles", "Qwen_Qwen2.5-0.5B-Instruct.json"))
	if err != nil || doc == nil {
		t.Fatalf("testdata profile: %v", err)
	}
	if err := cli.WriteProfileConfigMap(ctx, crdNS, qwen, *doc); err != nil {
		t.Fatalf("configmap: %v", err)
	}

	rep, err = operator.Run(ctx, cli, scoped, opt)
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
	if got.Status.Phase != "Ready" || got.Status.Assigned.Mode != "elastic" {
		t.Fatalf("after profile: phase=%s mode=%s reason=%s",
			got.Status.Phase, got.Status.Assigned.Mode, got.Status.Reason)
	}
	if got.Status.Assigned.ResidentLayers < 2 {
		t.Fatalf("expected packed layers, got %d", got.Status.Assigned.ResidentLayers)
	}
}

func em(name, model, budget string, prio int, ttft string, sessions, ctxTok int) v1alpha1.ElasticModel {
	return v1alpha1.ElasticModel{
		APIVersion: v1alpha1.API,
		Kind:       v1alpha1.Kind,
		Metadata:   v1alpha1.ObjectMeta{Name: name, Namespace: crdNS},
		Spec: v1alpha1.Spec{
			Model:            model,
			MemoryBudget:     budget,
			Priority:         prio,
			TTFTSLO:          ttft,
			MaxSessions:      sessions,
			MaxContextTokens: ctxTok,
		},
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
