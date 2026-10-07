package reconcile

import (
	"testing"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/profile"
)

func qwenDoc() profile.Doc {
	return profile.Doc{
		Model: "Qwen/Qwen2.5-0.5B-Instruct", ProcessFloorMB: 250, NumLayers: 24,
		Elastic:  profile.Peak{DriverMB: 680, TokensPerS: 171, TTFTMs: 46, StepMs: 5.8, ResidentLayers: 1, Batch: 1},
		Resident: profile.Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, StepMs: 1.5, ResidentLayers: 24, Batch: 1},
		Swap:     profile.Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, SwitchMs: 80, Batch: 1},
	}
}

func smolDoc() profile.Doc {
	return profile.Doc{
		Model: "HuggingFaceTB/SmolLM2-360M-Instruct", ProcessFloorMB: 250, NumLayers: 32,
		Elastic:  profile.Peak{DriverMB: 447, TokensPerS: 200, TTFTMs: 30, StepMs: 5.0, ResidentLayers: 1, Batch: 1},
		Resident: profile.Peak{DriverMB: 974, TokensPerS: 704, TTFTMs: 10, StepMs: 1.4, ResidentLayers: 32, Batch: 1},
		Swap:     profile.Peak{DriverMB: 974, TokensPerS: 704, TTFTMs: 10, SwitchMs: 60, Batch: 1},
	}
}

func cr(name, model, budget string, prio int) Item {
	return Item{Generation: 1, Model: v1alpha1.ElasticModel{
		Metadata: v1alpha1.ObjectMeta{Name: name, Namespace: "llc"},
		Spec: v1alpha1.Spec{
			Model: model, MemoryBudget: budget, Priority: prio,
			MaxSessions: 1, MaxContextTokens: 512,
		},
	}}
}

func TestAdmitAndReject(t *testing.T) {
	profs := map[string]profile.Doc{
		"Qwen/Qwen2.5-0.5B-Instruct":          qwenDoc(),
		"HuggingFaceTB/SmolLM2-360M-Instruct": smolDoc(),
	}
	items := []Item{
		cr("chat", "Qwen/Qwen2.5-0.5B-Instruct", "1400Mi", 10),
		cr("tiny", "Qwen/Qwen2.5-0.5B-Instruct", "400Mi", 1),
	}
	rep := Run(items, Options{DeviceBudgetMB: 4096, Profiles: profs})
	if phase("chat", rep) != "Ready" {
		t.Fatalf("chat: %+v", status("chat", rep))
	}
	if phase("tiny", rep) != "Rejected" {
		t.Fatalf("tiny should reject: %+v", status("tiny", rep))
	}
	if rep.AssignedMB > rep.DeviceBudgetMB {
		t.Fatalf("oversubscribed %f", rep.AssignedMB)
	}
}

func TestElasticPacksLayers(t *testing.T) {
	profs := map[string]profile.Doc{"Qwen/Qwen2.5-0.5B-Instruct": qwenDoc()}
	items := []Item{cr("chat", "Qwen/Qwen2.5-0.5B-Instruct", "1400Mi", 10)}
	rep := Run(items, Options{DeviceBudgetMB: 1200, Profiles: profs})
	st := status("chat", rep)
	if st.Phase != "Ready" || st.Assigned.Mode != "elastic" {
		t.Fatalf("want elastic Ready: %+v", st)
	}
	if st.Assigned.ResidentLayers <= 1 {
		t.Fatalf("should pack more than one layer: %+v", st.Assigned)
	}
}

func TestPriority(t *testing.T) {
	profs := map[string]profile.Doc{"Qwen/Qwen2.5-0.5B-Instruct": qwenDoc()}
	items := []Item{
		cr("low", "Qwen/Qwen2.5-0.5B-Instruct", "1400Mi", 1),
		cr("high", "Qwen/Qwen2.5-0.5B-Instruct", "1400Mi", 100),
	}
	rep := Run(items, Options{DeviceBudgetMB: 1300, Profiles: profs})
	if phase("high", rep) != "Ready" || phase("low", rep) == "Ready" {
		t.Fatalf("high=%s low=%s", phase("high", rep), phase("low", rep))
	}
}

func phase(name string, r Report) string { return status(name, r).Phase }

func status(name string, r Report) v1alpha1.Status {
	for _, m := range r.Models {
		if m.Name == name {
			return m.Status
		}
	}
	return v1alpha1.Status{}
}
