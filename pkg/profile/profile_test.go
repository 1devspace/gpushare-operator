package profile

import (
	"testing"

	"gpushare-elastic/pkg/modes"
)

func qwen() Doc {
	return Doc{
		Model: "Qwen/Qwen2.5-0.5B-Instruct", ProcessFloorMB: 250, NumLayers: 24,
		Elastic:  Peak{DriverMB: 680, TokensPerS: 171, TTFTMs: 46, StepMs: 5.8, ResidentLayers: 1, Batch: 1},
		Resident: Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, StepMs: 1.5, ResidentLayers: 24, Batch: 1},
		Swap:     Peak{DriverMB: 1391, TokensPerS: 650, TTFTMs: 12, SwitchMs: 80, Batch: 1},
	}
}

func smol() Doc {
	return Doc{
		Model: "HuggingFaceTB/SmolLM2-360M-Instruct", ProcessFloorMB: 250, NumLayers: 32,
		Elastic:  Peak{DriverMB: 447, TokensPerS: 200, TTFTMs: 30, StepMs: 5.0, ResidentLayers: 1, Batch: 1},
		Resident: Peak{DriverMB: 974, TokensPerS: 704, TTFTMs: 10, StepMs: 1.4, ResidentLayers: 32, Batch: 1},
		Swap:     Peak{DriverMB: 974, TokensPerS: 704, TTFTMs: 10, SwitchMs: 60, Batch: 1},
	}
}

func TestComposeMemoryOrder(t *testing.T) {
	pts := ComposeDevice([]Doc{qwen(), smol()})
	by := map[string]float64{}
	for _, p := range pts {
		by[p.Mode] = p.RequiredMB()
	}
	if by["elastic"] >= by["swap"] || by["swap"] >= by["resident"] {
		t.Fatalf("order %+v", by)
	}
	if pts[1].Mode != "swap" || pts[1].SwitchMs != 80 {
		t.Fatalf("swap switch %v", pts)
	}
}

func TestComposeThenChoose(t *testing.T) {
	pts := ComposeDevice([]Doc{qwen(), smol()})
	by := map[string]float64{}
	for _, p := range pts {
		by[p.Mode] = p.RequiredMB()
	}
	tight := modes.Choose(pts, modes.ChooseArgs{BudgetMB: by["elastic"] + 40, OutputTokens: 16, RunLength: 4})
	mid := modes.Choose(pts, modes.ChooseArgs{BudgetMB: by["swap"] + 40, OutputTokens: 16, RunLength: 4})
	roomy := modes.Choose(pts, modes.ChooseArgs{BudgetMB: by["resident"] + 40, OutputTokens: 16, RunLength: 4})
	if tight.Mode != "elastic" || mid.Mode != "swap" || roomy.Mode != "resident" {
		t.Fatalf("tight=%s mid=%s roomy=%s", tight.Mode, mid.Mode, roomy.Mode)
	}
}

func TestCapabilitiesIncludePackedR(t *testing.T) {
	caps := Capabilities(qwen())
	maxR := 0
	for _, c := range caps {
		if c.Mode == "streamed" && c.ResidentLayers > maxR {
			maxR = c.ResidentLayers
		}
	}
	if maxR <= 1 {
		t.Fatal("expected synthesised r>1")
	}
}
