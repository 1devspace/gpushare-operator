package packing

import (
	"math"
	"testing"
)

func TestDeriveAndFootprint(t *testing.T) {
	c := Derive(24, 800, 1800, 250, nil, nil)
	wantLayer := (1800.0 - 800.0) / 23
	if math.Abs(c.LayerMB-wantLayer) > 0.01 {
		t.Fatalf("layer %f want %f", c.LayerMB, wantLayer)
	}
	if math.Abs(c.FootprintMB(1)-800) > 1 {
		t.Fatalf("r1 %f", c.FootprintMB(1))
	}
	if math.Abs(c.FootprintMB(24)-1800) > 1 {
		t.Fatalf("r24 %f", c.FootprintMB(24))
	}
	m := 60.0
	bigger := Derive(24, 800, 1800, 250, nil, &m)
	if bigger.LayerMB < 60 {
		t.Fatalf("measured layer should win: %f", bigger.LayerMB)
	}
}

func TestPackGrowsWithBudget(t *testing.T) {
	c := Derive(24, 800, 1800, 250, nil, nil)
	tiny := Layers(c, 400, 0, 32, 150, 650)
	if tiny.Feasible {
		t.Fatal("400 MB should reject")
	}
	r1 := Layers(c, 832, 0, 32, 150, 650)
	if !r1.Feasible || r1.ResidentLayers != 1 {
		t.Fatalf("r1: %+v", r1)
	}
	mid := Layers(c, 1200, 0, 32, 150, 650)
	if !mid.Feasible || mid.ResidentLayers <= 1 || mid.Pin {
		t.Fatalf("mid: %+v", mid)
	}
	if mid.PredictedMB > 1200-32 {
		t.Fatalf("over budget %f", mid.PredictedMB)
	}
	roomy := Layers(c, 1400, 0, 32, 150, 650)
	if roomy.ResidentLayers < mid.ResidentLayers {
		t.Fatalf("larger budget packed fewer layers")
	}
	if roomy.NumGroups > mid.NumGroups {
		t.Fatalf("larger pack should have fewer groups")
	}
	full := Layers(c, 2000, 0, 32, 150, 650)
	if !full.Pin || full.ResidentLayers != 24 {
		t.Fatalf("full: %+v", full)
	}
	withKV := Layers(c, 1400, 200, 32, 150, 650)
	if withKV.ResidentLayers > roomy.ResidentLayers {
		t.Fatalf("KV reserve should shrink pack")
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokensPerS(150, 650, 24, 1) != 150 {
		t.Fatal("r=1")
	}
	if EstimateTokensPerS(150, 650, 24, 24) != 650 {
		t.Fatal("r=all")
	}
	mid := EstimateTokensPerS(150, 650, 24, 6)
	if mid <= 150 || mid >= 650 {
		t.Fatalf("mid %f", mid)
	}
}
