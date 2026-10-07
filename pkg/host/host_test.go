package host

import "testing"

func TestReshapeRollback(t *testing.T) {
	h := New(900)
	_ = h.Add("a", ModelCost{ResidentMB: 800, AlwaysMB: 400, LayerMB: 40, NumLayers: 10}, Placement{Mode: "elastic", ResidentLayers: 1})
	_ = h.Add("b", ModelCost{ResidentMB: 700, AlwaysMB: 350, LayerMB: 30, NumLayers: 10}, Placement{Mode: "elastic", ResidentLayers: 1})
	before := h.Placement("a")
	res := h.Reshape(map[string]Placement{
		"a": {Mode: "resident"},
		"b": {Mode: "resident"},
	})
	if res.OK {
		t.Fatal("two resident models should OOM")
	}
	if !res.RolledBack {
		t.Fatal("must roll back")
	}
	if h.Placement("a") != before {
		t.Fatalf("a changed after failed reshape: %+v", h.Placement("a"))
	}
	ok := h.Reshape(map[string]Placement{
		"a": {Mode: "resident"},
		"b": {Mode: "parked"},
	})
	if !ok.OK {
		t.Fatalf("swap-shaped reshape should fit: %s", ok.Error)
	}
}
