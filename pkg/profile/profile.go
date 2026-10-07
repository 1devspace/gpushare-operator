// Package profile reads measured auto-profiles and composes device mode points.
package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gpushare-elastic/pkg/modes"
	"gpushare-elastic/pkg/packing"
	"gpushare-elastic/pkg/policy"
)

type Peak struct {
	DriverMB       float64 `json:"driver_mb"`
	TokensPerS     float64 `json:"tokens_per_s"`
	TTFTMs         float64 `json:"ttft_ms"`
	StepMs         float64 `json:"step_ms"`
	ResidentLayers int     `json:"resident_layers"`
	Batch          int     `json:"batch"`
	SwitchMs       float64 `json:"switch_ms"`
}

type LayerCostDoc struct {
	NumLayers        int     `json:"num_layers"`
	ProcessFloorMB   float64 `json:"process_floor_mb"`
	AlwaysResidentMB float64 `json:"always_resident_mb"`
	LayerMB          float64 `json:"layer_mb"`
	Source           string  `json:"source"`
}

type Doc struct {
	Model          string       `json:"model"`
	Source         string       `json:"source"`
	ProcessFloorMB float64      `json:"process_floor_mb"`
	NumLayers      int          `json:"num_layers"`
	LayerCost      LayerCostDoc `json:"layer_cost"`
	Elastic        Peak         `json:"elastic"`
	Resident       Peak         `json:"resident"`
	Swap           Peak         `json:"swap"`
	Batches        []Peak       `json:"batches"`
}

func (d Doc) OK() bool {
	return d.Elastic.DriverMB > 0 && d.Resident.DriverMB > 0
}

func FromBytes(raw []byte) (*Doc, error) {
	var d Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func ReadFile(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return FromBytes(raw)
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func Slug(modelID string) string {
	return strings.Trim(slugRE.ReplaceAllString(modelID, "_"), "_")
}

func CachePath(modelID, autoDir string) string {
	return filepath.Join(autoDir, Slug(modelID)+".json")
}

func LoadCached(modelID, autoDir string) (*Doc, error) {
	doc, err := ReadFile(CachePath(modelID, autoDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}

func LayerCost(d Doc) *packing.LayerCost {
	if !d.OK() {
		return nil
	}
	n := d.LayerCost.NumLayers
	if n < 1 {
		n = d.NumLayers
	}
	if n < 1 {
		n = d.Resident.ResidentLayers
	}
	if n < 1 {
		return nil
	}
	var measured, always *float64
	if d.LayerCost.LayerMB > 0 {
		v := d.LayerCost.LayerMB
		measured = &v
	}
	if d.LayerCost.AlwaysResidentMB > 0 {
		v := d.LayerCost.AlwaysResidentMB
		always = &v
	}
	floor := d.LayerCost.ProcessFloorMB
	if floor == 0 {
		floor = d.ProcessFloorMB
	}
	c := packing.Derive(n, d.Elastic.DriverMB, d.Resident.DriverMB, floor, always, measured)
	return &c
}

func Pack(d Doc, budgetMB, kvReserve, safetyMB float64) packing.Pack {
	c := LayerCost(d)
	if c == nil {
		return packing.Pack{Reason: "profile has no layer cost"}
	}
	return packing.Layers(*c, budgetMB, kvReserve, safetyMB, d.Elastic.TokensPerS, d.Resident.TokensPerS)
}

func Capabilities(d Doc) []policy.Capability {
	var out []policy.Capability
	if d.Elastic.DriverMB > 0 {
		b := d.Elastic.Batch
		if b == 0 {
			b = 1
		}
		r := d.Elastic.ResidentLayers
		if r == 0 {
			r = 1
		}
		mb := d.Elastic.DriverMB
		out = append(out, policy.Capability{
			Mode: "streamed", ResidentLayers: r, Batch: b,
			DriverMB: mb, TokensPerS: d.Elastic.TokensPerS, StepMs: d.Elastic.StepMs, MinBudgetMB: &mb,
		})
	}
	if d.Resident.DriverMB > 0 {
		b := d.Resident.Batch
		if b == 0 {
			b = 1
		}
		mb := d.Resident.DriverMB
		out = append(out, policy.Capability{
			Mode: "pinned", ResidentLayers: d.Resident.ResidentLayers, Batch: b,
			DriverMB: mb, TokensPerS: d.Resident.TokensPerS, StepMs: d.Resident.StepMs, MinBudgetMB: &mb,
		})
	}
	cost := LayerCost(d)
	if cost != nil && d.Elastic.DriverMB > 0 && d.Resident.DriverMB > 0 {
		seen := map[int]bool{}
		for _, c := range out {
			if c.Mode == "streamed" {
				seen[c.ResidentLayers] = true
			}
		}
		for r := 2; r < cost.NumLayers; r *= 2 {
			if seen[r] {
				continue
			}
			tps := packing.EstimateTokensPerS(d.Elastic.TokensPerS, d.Resident.TokensPerS, cost.NumLayers, r)
			step := 0.0
			if tps > 0 {
				step = 1000.0 / tps
			}
			mb := cost.FootprintMB(r)
			out = append(out, policy.Capability{
				Mode: "streamed", ResidentLayers: r, Batch: 1,
				DriverMB: mb, TokensPerS: tps, StepMs: step, MinBudgetMB: &mb,
			})
		}
	}
	return out
}

func ComposeDevice(docs []Doc) []modes.Point {
	var usable []Doc
	for _, d := range docs {
		if d.OK() {
			usable = append(usable, d)
		}
	}
	if len(usable) == 0 {
		return nil
	}
	floor := usable[0].ProcessFloorMB
	for _, d := range usable[1:] {
		if d.ProcessFloorMB > floor {
			floor = d.ProcessFloorMB
		}
	}
	above := func(driver float64) float64 {
		v := driver - floor
		if v < 0 {
			return 0
		}
		return v
	}
	elasticMB := floor
	residentMB := floor
	swapMB := usable[0].Resident.DriverMB
	switchMs := usable[0].Swap.SwitchMs
	elTPS := usable[0].Elastic.TokensPerS
	rsTPS := usable[0].Resident.TokensPerS
	elTTFT := usable[0].Elastic.TTFTMs
	rsTTFT := usable[0].Resident.TTFTMs
	for i, d := range usable {
		elasticMB += above(d.Elastic.DriverMB)
		residentMB += above(d.Resident.DriverMB)
		if d.Resident.DriverMB > swapMB {
			swapMB = d.Resident.DriverMB
		}
		if d.Swap.SwitchMs > switchMs {
			switchMs = d.Swap.SwitchMs
		}
		if i == 0 {
			continue
		}
		if d.Elastic.TokensPerS < elTPS {
			elTPS = d.Elastic.TokensPerS
		}
		if d.Resident.TokensPerS < rsTPS {
			rsTPS = d.Resident.TokensPerS
		}
		if d.Elastic.TTFTMs > elTTFT {
			elTTFT = d.Elastic.TTFTMs
		}
		if d.Resident.TTFTMs > rsTTFT {
			rsTTFT = d.Resident.TTFTMs
		}
	}
	n := len(usable)
	pt := func(mode string, driver, tps, ttft, sw float64) modes.Point {
		return modes.Point{
			Mode: mode, Concurrency: 1, RunLength: 1, NumModels: n,
			MinBudgetMB: driver, DriverMB: driver, RawTokensPerS: tps,
			TTFTMs: ttft, SwitchMs: sw,
		}
	}
	return []modes.Point{
		pt("elastic", elasticMB, elTPS, elTTFT, 0),
		pt("swap", swapMB, rsTPS, rsTTFT, switchMs),
		pt("resident", residentMB, rsTPS, rsTTFT, 0),
	}
}
