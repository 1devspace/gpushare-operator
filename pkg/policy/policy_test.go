package policy

import "testing"

func TestChooseHighestThroughputThatFits(t *testing.T) {
	caps := []Capability{
		{Mode: "streamed", ResidentLayers: 1, Batch: 1, DriverMB: 800, TokensPerS: 150, StepMs: 6.6},
		{Mode: "streamed", ResidentLayers: 4, Batch: 1, DriverMB: 1000, TokensPerS: 300, StepMs: 3.3},
		{Mode: "pinned", ResidentLayers: 24, Batch: 1, DriverMB: 1800, TokensPerS: 650, StepMs: 1.5},
	}
	d := Choose(caps, 1400, nil, nil, 0, 32)
	if !d.Feasible || d.ResidentLayers != 4 {
		t.Fatalf("want streamed r4, got %+v", d)
	}
	tiny := Choose(caps, 300, nil, nil, 0, 32)
	if tiny.Feasible {
		t.Fatal("300 MB should reject")
	}
	step := 2.0
	slo := Choose(caps, 2400, &step, nil, 0, 32)
	if !slo.Feasible || slo.Mode != "pinned" {
		t.Fatalf("tight step SLO: %+v", slo)
	}
	impossible := 0.5
	bad := Choose(caps, 2400, &impossible, nil, 0, 32)
	if bad.Feasible {
		t.Fatal("unreachable SLO should reject")
	}
}

func TestKVReserveMB(t *testing.T) {
	got := KVReserveMB(15872, 2048, 8)
	if got < 240 || got > 260 {
		t.Fatalf("got %f", got)
	}
}
