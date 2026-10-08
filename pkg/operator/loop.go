// Package operator is the control plane, NVIDIA GPU Operator style.
//
// The Deployment that runs this code has no GPU. On each reconcile it:
//  1. lists ElasticModel objects
//  2. for each unseen model, ensures a profiler Job operand on a GPU node
//  3. reads the profile ConfigMap that Job wrote
//  4. chooses resident / swap / elastic and writes status
//
// Same split as ClusterPolicy + nvidia-operator-validator: the operator
// only creates operands; the operand does the device work.
package operator

import (
	"context"
	"fmt"
	"time"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/modes"
	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/policy"
	"gpushare-elastic/pkg/profile"
	"gpushare-elastic/pkg/quantity"
	"gpushare-elastic/pkg/reconcile"
)

type Kube interface {
	LoadProfiles(ctx context.Context, namespace string, modelIDs []string) (map[string]profile.Doc, error)
	EnsureProfilerJob(ctx context.Context, namespace, modelID string, spec profiler.Spec) (profiler.JobStatus, error)
}

type Options struct {
	Namespace      string
	DeviceBudgetMB float64
	Profiles       map[string]profile.Doc
	Capabilities   []policy.Capability
	ModeProfile    []modes.Point
	WorkerURL      string
	SafetyMB       float64
	Profiler       profiler.Spec
	CreateJobs     bool
}

func Run(ctx context.Context, kube Kube, items []reconcile.Item, opt Options) (reconcile.Report, error) {
	ids := uniqueModels(items)
	profiles := map[string]profile.Doc{}
	for k, v := range opt.Profiles {
		profiles[k] = v
	}
	if kube != nil {
		fromCM, err := kube.LoadProfiles(ctx, opt.Namespace, ids)
		if err != nil {
			return reconcile.Report{}, err
		}
		for k, v := range fromCM {
			if _, ok := profiles[k]; !ok {
				profiles[k] = v
			}
		}
	}

	var pending []reconcile.Result
	var ready []reconcile.Item
	started := map[string]profiler.JobStatus{}
	now := time.Now().UTC().Format(time.RFC3339)

	for _, it := range items {
		id := it.Model.Spec.Model
		if id != "" {
			if _, ok := profiles[id]; ok {
				ready = append(ready, it)
				continue
			}
		}
		budget, err := quantity.ParseMB(it.Model.Spec.MemoryBudget)
		if err != nil {
			pending = append(pending, reject(it, budget, err.Error(), now))
			continue
		}
		if id == "" {
			pending = append(pending, reject(it, budget, "spec.model is required", now))
			continue
		}
		st, err := ensureOnce(ctx, kube, opt, id, started)
		if err != nil {
			pending = append(pending, degrade(it, budget,
				fmt.Sprintf("could not create profiler Job: %v", err), now))
			continue
		}
		if st.FailedCompletely() {
			pending = append(pending, degrade(it, budget,
				fmt.Sprintf("profiler Job %s failed; it will be recreated on the next reconcile", st.Name), now))
			continue
		}
		reason := fmt.Sprintf("measuring %s on this GPU (Job %s)", id, profiler.JobName(id))
		if !opt.CreateJobs || kube == nil {
			reason = fmt.Sprintf("waiting for first-seen profile ConfigMap %s", profiler.ConfigMapName(id))
		}
		pending = append(pending, profiling(it, budget, reason, now))
	}

	report := reconcile.Run(ready, reconcile.Options{
		DeviceBudgetMB: opt.DeviceBudgetMB,
		Profiles:       profiles,
		Capabilities:   opt.Capabilities,
		ModeProfile:    opt.ModeProfile,
		WorkerURL:      opt.WorkerURL,
		SafetyMB:       opt.SafetyMB,
	})
	for i := range report.Models {
		markAssigned(&report.Models[i].Status, now)
	}
	merged := append([]reconcile.Result{}, pending...)
	merged = append(merged, report.Models...)
	report.Models = merged
	return report, nil
}

func ensureOnce(ctx context.Context, kube Kube, opt Options, modelID string, started map[string]profiler.JobStatus) (profiler.JobStatus, error) {
	if st, ok := started[modelID]; ok {
		return st, nil
	}
	if kube == nil || !opt.CreateJobs {
		return profiler.JobStatus{}, nil
	}
	st, err := kube.EnsureProfilerJob(ctx, opt.Namespace, modelID, opt.Profiler)
	if err != nil {
		return st, err
	}
	started[modelID] = st
	return st, nil
}

func uniqueModels(items []reconcile.Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		id := it.Model.Spec.Model
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func profiling(it reconcile.Item, budget float64, reason, now string) reconcile.Result {
	return reconcile.Result{
		Name: it.Model.Metadata.Name, BudgetMB: budget,
		Status: v1alpha1.Status{
			Phase:              "Profiling",
			Reason:             reason,
			ObservedGeneration: it.Generation,
			Conditions: []v1alpha1.Condition{
				{Type: "ProfileAvailable", Status: "False", Reason: "Measuring", Message: reason, LastTransitionTime: now},
				{Type: "ModeAssigned", Status: "False", Reason: "WaitingForProfile", LastTransitionTime: now},
			},
		},
	}
}

func degrade(it reconcile.Item, budget float64, reason, now string) reconcile.Result {
	return reconcile.Result{
		Name: it.Model.Metadata.Name, BudgetMB: budget,
		Status: v1alpha1.Status{
			Phase:              "Degraded",
			Reason:             reason,
			ObservedGeneration: it.Generation,
			Conditions: []v1alpha1.Condition{
				{Type: "ProfileAvailable", Status: "False", Reason: "ProfilerFailed", Message: reason, LastTransitionTime: now},
			},
		},
	}
}

func reject(it reconcile.Item, budget float64, reason, now string) reconcile.Result {
	return reconcile.Result{
		Name: it.Model.Metadata.Name, BudgetMB: budget,
		Status: v1alpha1.Status{
			Phase:              "Rejected",
			Reason:             reason,
			ObservedGeneration: it.Generation,
			Conditions: []v1alpha1.Condition{
				{Type: "ModeAssigned", Status: "False", Reason: "Rejected", Message: reason, LastTransitionTime: now},
			},
		},
	}
}

func markAssigned(st *v1alpha1.Status, now string) {
	if st.Phase != "Ready" && st.Phase != "Rejected" && st.Phase != "Degraded" {
		return
	}
	if len(st.Conditions) > 0 {
		return
	}
	ok := "True"
	if st.Phase != "Ready" {
		ok = "False"
	}
	st.Conditions = []v1alpha1.Condition{
		{Type: "ProfileAvailable", Status: "True", Reason: "Measured", LastTransitionTime: now},
		{Type: "ModeAssigned", Status: ok, Reason: st.Phase, Message: st.Reason, LastTransitionTime: now},
	}
}
