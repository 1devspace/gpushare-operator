// Elastic occupancy operator: watches ElasticModel objects and assigns
// resident / swap / elastic from a measured profile. The user never sets mode.
//
// On a cluster the first unseen model starts a profiler Job on a GPU node.
// That Job writes a ConfigMap; the next reconcile chooses the mode.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gpushare-elastic/pkg/cluster"
	"gpushare-elastic/pkg/manifest"
	"gpushare-elastic/pkg/modes"
	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/operator"
	"gpushare-elastic/pkg/policy"
	"gpushare-elastic/pkg/reconcile"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	var (
		namespace     = flag.String("namespace", "llc", "ElasticModel namespace")
		deviceBudget  = flag.Float64("device-budget-mb", 4096, "total GPU MB the operator may assign")
		fromDir       = flag.String("from-dir", "", "read YAML from a directory instead of the API server")
		profileDir    = flag.String("profile-dir", "", "optional extra directory of profile JSON (fallback)")
		capabilities  = flag.String("capabilities", "", "comma-separated capability JSON paths")
		modeProfile   = flag.String("mode-profile", "", "comma-separated mode-profile JSON paths")
		workerURL     = flag.String("worker-url", "", "serving worker that accepts POST /admin/config")
		kubeconfig    = flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig; empty uses in-cluster")
		interval      = flag.Duration("interval", 0, "reconcile period; 0 runs once")
		out           = flag.String("out", "", "write the last report as JSON")
		dryRun        = flag.Bool("dry-run", false, "do not patch status (implied by --from-dir)")
		noProfileJobs = flag.Bool("no-profile-jobs", false, "do not create first-seen profiler Jobs")
		profilerImage = flag.String("profiler-image", "", "image for the profiler Job")
		profilerStub  = flag.Bool("profiler-stub", false, "profiler Job writes a stub profile (no GPU)")
		workspacePVC  = flag.String("workspace-pvc", "llc-code", "PVC with GPUShare-LLM (profile_job.py)")
		hfCachePVC    = flag.String("hf-cache-pvc", "llc-hf", "PVC for HuggingFace weights")
		gpuDevices    = flag.Bool("gpu-devices", true, "mount /dev/kfd and /dev/dri on the profiler Job")
		nodeSelector  = flag.String("gpu-node-selector", "", "comma-separated key=value for the profiler Job")
	)
	flag.Parse()

	caps, err := loadCaps(*capabilities)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	modePts, err := loadModes(*modeProfile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	offline := *fromDir != ""
	createJobs := !offline && !*noProfileJobs
	var cli *cluster.Client
	if !offline {
		cfg, err := kubeCfg(*kubeconfig)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cli, err = cluster.New(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	var kube operator.Kube
	if cli != nil {
		kube = cli
	}

	profSpec := profiler.Spec{
		Image:        *profilerImage,
		WorkspacePVC: *workspacePVC,
		HFCachePVC:   *hfCachePVC,
		GPUDevices:   *gpuDevices && !*profilerStub,
		Stub:         *profilerStub,
		NodeSelector: parseSelector(*nodeSelector),
	}
	if *profilerStub {
		profSpec.WorkspacePVC = ""
		profSpec.HFCachePVC = ""
	}

	var last reconcile.Report
	for {
		items, err := loadItems(*fromDir, *namespace, cli)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.Model.Spec.Model)
		}
		profs := reconcile.LoadProfilesFromDir(*profileDir, ids)
		fmt.Printf("\nreconcile @ %s\n", time.Now().Format("15:04:05"))
		last, err = operator.Run(context.Background(), kube, items, operator.Options{
			Namespace:      *namespace,
			DeviceBudgetMB: *deviceBudget,
			Profiles:       profs,
			Capabilities:   caps,
			ModeProfile:    modePts,
			WorkerURL:      *workerURL,
			Profiler:       profSpec,
			CreateJobs:     createJobs,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		printReport(last)
		if !offline && !*dryRun && cli != nil {
			if err := cli.PatchStatuses(context.Background(), *namespace, last); err != nil {
				fmt.Fprintf(os.Stderr, "status patch: %v\n", err)
			}
		}
		if *out != "" {
			raw, _ := json.MarshalIndent(last, "", "  ")
			_ = os.WriteFile(*out, append(raw, '\n'), 0o644)
			fmt.Println("Wrote", *out)
		}
		if *interval <= 0 {
			break
		}
		time.Sleep(*interval)
	}
}

func loadItems(fromDir, namespace string, cli *cluster.Client) ([]reconcile.Item, error) {
	if fromDir != "" {
		return manifest.FromDir(fromDir)
	}
	if cli == nil {
		return nil, fmt.Errorf("no cluster client")
	}
	return cli.List(context.Background(), namespace)
}

func kubeCfg(kubeconfig string) (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func loadCaps(csv string) ([]policy.Capability, error) {
	if csv == "" {
		return nil, nil
	}
	return policy.LoadCapabilities(splitCSV(csv)...)
}

func loadModes(csv string) ([]modes.Point, error) {
	if csv == "" {
		return nil, nil
	}
	return modes.LoadProfile(splitCSV(csv)...)
}

func parseSelector(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range splitCSV(s) {
		k, v, ok := strings.Cut(part, "=")
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printReport(r reconcile.Report) {
	if r.DeviceMode != nil {
		d := r.DeviceMode
		if d.Feasible {
			fmt.Printf("  device mode: %s (%.0f tok/s, %.0f ms first token, %.0f MB) - %s\n",
				d.Mode, d.PredictedTokensPerS, d.PredictedTTFTMs, d.RequiredMB, d.Reason)
			for mode, why := range d.Considered {
				if mode != d.Mode {
					fmt.Printf("    %-9s not chosen: %s\n", mode, why)
				}
			}
		} else {
			fmt.Printf("  device mode: none viable - %s\n", d.Reason)
		}
	}
	ready := 0
	for _, m := range r.Models {
		if m.Status.Phase == "Ready" {
			ready++
			a := m.Status.Assigned
			fmt.Printf("  %-14s Ready     %-17s %7.0f MB +%6.1f KV  %7.1f tok/s\n",
				m.Name, a.Mode, a.PredictedDriverMB, a.KVReserveMB, a.PredictedTokensPerSecond)
		} else {
			fmt.Printf("  %-14s %-9s %s\n", m.Name, m.Status.Phase, trim(m.Status.Reason, 64))
		}
	}
	fmt.Printf("\n  device %.0f MB: %.0f MB assigned, %.0f MB free, %d/%d admitted\n",
		r.DeviceBudgetMB, r.AssignedMB, r.FreeMB, ready, len(r.Models))
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
