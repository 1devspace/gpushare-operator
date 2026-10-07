// Package host is the in-process placement contract: reshape is atomic.
// A failure restores the previous placement. This is the worker-as-authority
// rule, without a GPU — estimated footprints stand in for driver MB.
package host

import (
	"fmt"
	"sort"
	"time"
)

type Placement struct {
	Mode           string
	ResidentLayers int
	HeadResident   bool
}

func (p Placement) Label() string {
	switch p.Mode {
	case "resident", "pinned":
		return "resident"
	case "parked":
		return "parked"
	case "swap":
		return "swap"
	default:
		r := p.ResidentLayers
		if r < 1 {
			r = 1
		}
		return fmt.Sprintf("elastic r%d", r)
	}
}

type ReshapeResult struct {
	OK         bool
	Applied    map[string]Placement
	ElapsedMs  float64
	Error      string
	RolledBack bool
}

type ModelCost struct {
	ResidentMB float64
	LayerMB    float64
	AlwaysMB   float64
	NumLayers  int
}

type Host struct {
	BudgetMB   float64
	Costs      map[string]ModelCost
	placements map[string]Placement
}

func New(budgetMB float64) *Host {
	return &Host{
		BudgetMB:   budgetMB,
		Costs:      map[string]ModelCost{},
		placements: map[string]Placement{},
	}
}

func (h *Host) Add(name string, cost ModelCost, place Placement) error {
	if place.Mode == "" {
		place.Mode = "elastic"
	}
	if place.ResidentLayers < 1 {
		place.ResidentLayers = 1
	}
	h.Costs[name] = cost
	h.placements[name] = place
	if h.usedMB() > h.BudgetMB && h.BudgetMB > 0 {
		delete(h.Costs, name)
		delete(h.placements, name)
		return fmt.Errorf("OutOfMemoryError: model %q does not fit", name)
	}
	return nil
}

func (h *Host) Placement(name string) Placement {
	return h.placements[name]
}

func (h *Host) Names() []string {
	out := make([]string, 0, len(h.placements))
	for n := range h.placements {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (h *Host) usedMB() float64 {
	var sum float64
	for name, p := range h.placements {
		sum += h.costOf(name, p)
	}
	return sum
}

func (h *Host) costOf(name string, p Placement) float64 {
	c := h.Costs[name]
	switch p.Mode {
	case "parked":
		return 0
	case "resident", "pinned", "swap":
		if c.ResidentMB > 0 {
			return c.ResidentMB
		}
		return c.AlwaysMB + float64(c.NumLayers)*c.LayerMB
	default:
		r := p.ResidentLayers
		if r < 1 {
			r = 1
		}
		if c.NumLayers > 0 && r > c.NumLayers {
			r = c.NumLayers
		}
		if c.AlwaysMB > 0 || c.LayerMB > 0 {
			return c.AlwaysMB + float64(r)*c.LayerMB
		}
		return c.ResidentMB
	}
}

func rank(mode string) int {
	switch mode {
	case "parked":
		return 0
	case "elastic", "streamed":
		return 1
	default:
		return 2
	}
}

// Reshape applies placements atomically. On failure every named model is
// restored, including the one that failed.
func (h *Host) Reshape(assignments map[string]Placement) ReshapeResult {
	t0 := time.Now()
	prev := map[string]Placement{}
	for k, v := range h.placements {
		prev[k] = v
	}
	type pair struct {
		name  string
		place Placement
	}
	ordered := make([]pair, 0, len(assignments))
	for n, p := range assignments {
		ordered = append(ordered, pair{n, p})
	}
	sort.Slice(ordered, func(i, j int) bool {
		return rank(ordered[i].place.Mode) < rank(ordered[j].place.Mode)
	})

	apply := func(as map[string]Placement) error {
		for _, kv := range ordered {
			if _, ok := h.Costs[kv.name]; !ok {
				return fmt.Errorf("unknown model %q", kv.name)
			}
			p := kv.place
			if p.ResidentLayers < 1 {
				p.ResidentLayers = 1
			}
			h.placements[kv.name] = p
		}
		if h.BudgetMB > 0 && h.usedMB() > h.BudgetMB {
			return fmt.Errorf("OutOfMemoryError: reshape needs %.0f MB, budget %.0f MB", h.usedMB(), h.BudgetMB)
		}
		return nil
	}

	if err := apply(assignments); err != nil {
		rolled := true
		for name := range assignments {
			if p, ok := prev[name]; ok {
				h.placements[name] = p
			}
		}
		return ReshapeResult{
			Applied:    copyMap(h.placements),
			ElapsedMs:  float64(time.Since(t0).Microseconds()) / 1000,
			Error:      err.Error(),
			RolledBack: rolled,
		}
	}
	return ReshapeResult{
		OK:        true,
		Applied:   copyMap(h.placements),
		ElapsedMs: float64(time.Since(t0).Microseconds()) / 1000,
	}
}

func copyMap(in map[string]Placement) map[string]Placement {
	out := make(map[string]Placement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
