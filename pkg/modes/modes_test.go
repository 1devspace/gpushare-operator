package modes

import "testing"

func samplePoints() []Point {
	return []Point{
		{Mode: "elastic", Concurrency: 1, RunLength: 1, NumModels: 2, MinBudgetMB: 740, DriverMB: 740, RawTokensPerS: 171, TTFTMs: 46},
		{Mode: "swap", Concurrency: 1, RunLength: 1, NumModels: 2, MinBudgetMB: 1366, DriverMB: 1366, RawTokensPerS: 656, TTFTMs: 20, SwitchMs: 80},
		{Mode: "resident", Concurrency: 1, RunLength: 1, NumModels: 2, MinBudgetMB: 2050, DriverMB: 2050, RawTokensPerS: 704, TTFTMs: 12},
	}
}

func TestChooseModeMemoryOrder(t *testing.T) {
	pts := samplePoints()
	tight := Choose(pts, ChooseArgs{BudgetMB: 780, Concurrency: 1, OutputTokens: 16, RunLength: 4})
	if tight.Mode != "elastic" {
		t.Fatalf("tight: %s %s", tight.Mode, tight.Reason)
	}
	mid := Choose(pts, ChooseArgs{BudgetMB: 1406, Concurrency: 1, OutputTokens: 16, RunLength: 4})
	if mid.Mode != "swap" {
		t.Fatalf("mid: %s %s", mid.Mode, mid.Reason)
	}
	roomy := Choose(pts, ChooseArgs{BudgetMB: 2090, Concurrency: 1, OutputTokens: 16, RunLength: 4})
	if roomy.Mode != "resident" {
		t.Fatalf("roomy: %s %s", roomy.Mode, roomy.Reason)
	}
}

func TestChooseModeTTFTRulesOutSwap(t *testing.T) {
	pts := samplePoints()
	limit := 55.0
	d := Choose(pts, ChooseArgs{
		BudgetMB: 1800, Concurrency: 1, OutputTokens: 16, RunLength: 1, MaxTTFTMs: &limit,
	})
	if !d.Feasible || d.Mode != "elastic" {
		t.Fatalf("tight TTFT should pick elastic, got %+v", d)
	}
	if why := d.Considered["swap"]; why == "" || why[:4] != "fits" {
		t.Fatalf("swap should be rejected on TTFT: %q", why)
	}
}

func TestExpectedTTFTIncludesSwitch(t *testing.T) {
	p := Point{TTFTMs: 20, SwitchMs: 80}
	if ExpectedTTFTMs(p, 1) != 100 {
		t.Fatalf("got %f", ExpectedTTFTMs(p, 1))
	}
	if ExpectedTTFTMs(p, 4) != 40 {
		t.Fatalf("got %f", ExpectedTTFTMs(p, 4))
	}
}
