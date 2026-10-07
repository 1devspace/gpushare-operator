package operator

import (
	"context"
	"testing"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/profile"
	"gpushare-elastic/pkg/reconcile"
)

type fakeKube struct {
	profiles map[string]profile.Doc
	jobs     map[string]profiler.JobStatus
	created  []string
}

func (f *fakeKube) LoadProfiles(_ context.Context, _ string, _ []string) (map[string]profile.Doc, error) {
	out := map[string]profile.Doc{}
	for k, v := range f.profiles {
		out[k] = v
	}
	return out, nil
}

func (f *fakeKube) EnsureProfilerJob(_ context.Context, _, modelID string, _ profiler.Spec) (profiler.JobStatus, error) {
	f.created = append(f.created, modelID)
	if st, ok := f.jobs[modelID]; ok {
		return st, nil
	}
	st := profiler.JobStatus{Name: profiler.JobName(modelID), Exists: true, Active: 1}
	if f.jobs == nil {
		f.jobs = map[string]profiler.JobStatus{}
	}
	f.jobs[modelID] = st
	return st, nil
}

func qwen() profile.Doc {
	return profile.Doc{
		Model: "Qwen/Qwen2.5-0.5B-Instruct", ProcessFloorMB: 250, NumLayers: 24,
		Elastic:  profile.Peak{DriverMB: 680, TokensPerS: 171, TTFTMs: 46, StepMs: 5.8, ResidentLayers: 1, Batch: 1},
		Resident: profile.Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, StepMs: 1.5, ResidentLayers: 24, Batch: 1},
		Swap:     profile.Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, SwitchMs: 80, Batch: 1},
	}
}

func item(name, model, budget string) reconcile.Item {
	return reconcile.Item{Generation: 1, Model: v1alpha1.ElasticModel{
		Metadata: v1alpha1.ObjectMeta{Name: name, Namespace: "llc"},
		Spec:     v1alpha1.Spec{Model: model, MemoryBudget: budget, Priority: 10, MaxSessions: 1, MaxContextTokens: 512},
	}}
}

func TestFirstSeenCreatesOneJob(t *testing.T) {
	k := &fakeKube{}
	items := []reconcile.Item{
		item("chat", "Qwen/Qwen2.5-0.5B-Instruct", "1400Mi"),
		item("batch", "Qwen/Qwen2.5-0.5B-Instruct", "900Mi"),
	}
	rep, err := Run(context.Background(), k, items, Options{
		Namespace: "llc", DeviceBudgetMB: 4096, CreateJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(k.created) != 1 {
		t.Fatalf("expected one Job for the model, got %v", k.created)
	}
	for _, m := range rep.Models {
		if m.Status.Phase != "Profiling" {
			t.Fatalf("%s phase %s", m.Name, m.Status.Phase)
		}
	}
}

func TestProfileConfigMapChoosesMode(t *testing.T) {
	id := "Qwen/Qwen2.5-0.5B-Instruct"
	k := &fakeKube{profiles: map[string]profile.Doc{id: qwen()}}
	rep, err := Run(context.Background(), k, []reconcile.Item{item("chat", id, "1400Mi")}, Options{
		Namespace: "llc", DeviceBudgetMB: 1200, CreateJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(k.created) != 0 {
		t.Fatalf("must not re-measure: %v", k.created)
	}
	if len(rep.Models) != 1 || rep.Models[0].Status.Phase != "Ready" || rep.Models[0].Status.Assigned.Mode != "elastic" {
		t.Fatalf("%+v", rep.Models)
	}
}

func TestFailedJobIsDegraded(t *testing.T) {
	id := "Qwen/Qwen2.5-0.5B-Instruct"
	k := &fakeKube{jobs: map[string]profiler.JobStatus{
		id: {Name: profiler.JobName(id), Exists: true, Failed: 1},
	}}
	rep, err := Run(context.Background(), k, []reconcile.Item{item("chat", id, "1400Mi")}, Options{
		Namespace: "llc", DeviceBudgetMB: 4096, CreateJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Models[0].Status.Phase != "Degraded" {
		t.Fatalf("%+v", rep.Models[0].Status)
	}
}
