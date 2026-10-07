// Package policy picks residency and batch inside a memory budget.
package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

type Capability struct {
	Mode           string
	ResidentLayers int
	Batch          int
	DriverMB       float64
	TokensPerS     float64
	StepMs         float64
	MinBudgetMB    *float64
}

func (c Capability) RequiredMB() float64 {
	if c.MinBudgetMB != nil {
		return *c.MinBudgetMB
	}
	return c.DriverMB
}

func (c Capability) Label() string {
	if c.Mode == "pinned" {
		return fmt.Sprintf("resident b%d", c.Batch)
	}
	return fmt.Sprintf("streamed r%d b%d", c.ResidentLayers, c.Batch)
}

type Decision struct {
	Feasible            bool
	Reason              string
	Mode                string
	ResidentLayers      int
	Batch               int
	PredictedDriverMB   float64
	PredictedTokensPerS float64
	PredictedStepMs     float64
	KVReserveMB         float64
	HeadroomMB          float64
}

func (d Decision) Label() string {
	if !d.Feasible {
		return "none"
	}
	if d.Mode == "pinned" {
		return fmt.Sprintf("resident b%d", d.Batch)
	}
	return fmt.Sprintf("streamed r%d b%d", d.ResidentLayers, d.Batch)
}

func KVReserveMB(bytesPerToken, contextTokens, sessions int) float64 {
	return float64(bytesPerToken*contextTokens*sessions) / (1024 * 1024)
}

func Choose(caps []Capability, budgetMB float64, maxStepMs, minTokensPerS *float64, kvReserve, safetyMB float64) Decision {
	if safetyMB == 0 {
		safetyMB = 32
	}
	usable := budgetMB - kvReserve - safetyMB
	if len(caps) == 0 {
		return Decision{Reason: "no capability data"}
	}
	if usable <= 0 {
		return Decision{
			Reason: fmt.Sprintf(
				"no memory available: %.0f MB budget is consumed by %.0f MB KV reserve and %.0f MB safety margin",
				budgetMB, kvReserve, safetyMB,
			),
			KVReserveMB: kvReserve,
		}
	}
	var fits []Capability
	for _, c := range caps {
		if c.RequiredMB() <= usable {
			fits = append(fits, c)
		}
	}
	if len(fits) == 0 {
		cheapest := caps[0]
		for _, c := range caps[1:] {
			if c.RequiredMB() < cheapest.RequiredMB() {
				cheapest = c
			}
		}
		return Decision{
			Reason: fmt.Sprintf(
				"nothing fits %.0f MB usable (cheapest point needs %.0f MB)",
				usable, cheapest.RequiredMB(),
			),
			KVReserveMB: kvReserve,
		}
	}
	eligible := fits
	if maxStepMs != nil {
		var next []Capability
		for _, c := range eligible {
			if c.StepMs > 0 && c.StepMs <= *maxStepMs {
				next = append(next, c)
			}
		}
		eligible = next
	}
	if minTokensPerS != nil {
		var next []Capability
		for _, c := range eligible {
			if c.TokensPerS >= *minTokensPerS {
				next = append(next, c)
			}
		}
		eligible = next
	}
	if len(eligible) == 0 {
		best := fits[0]
		for _, c := range fits[1:] {
			if c.TokensPerS > best.TokensPerS {
				best = c
			}
		}
		return Decision{
			Reason: fmt.Sprintf(
				"budget fits but SLO unreachable; best within budget is %s at %.1f ms/step, %.1f tok/s",
				best.Label(), best.StepMs, best.TokensPerS,
			),
			KVReserveMB: kvReserve,
		}
	}
	best := eligible[0]
	for _, c := range eligible[1:] {
		if c.TokensPerS > best.TokensPerS || (c.TokensPerS == best.TokensPerS && c.RequiredMB() < best.RequiredMB()) {
			best = c
		}
	}
	return Decision{
		Feasible:            true,
		Reason:              "selected highest throughput within budget",
		Mode:                best.Mode,
		ResidentLayers:      best.ResidentLayers,
		Batch:               best.Batch,
		PredictedDriverMB:   best.RequiredMB(),
		PredictedTokensPerS: best.TokensPerS,
		PredictedStepMs:     best.StepMs,
		KVReserveMB:         kvReserve,
		HeadroomMB:          round(usable-best.RequiredMB(), 3),
	}
}

func LoadCapabilities(paths ...string) ([]Capability, error) {
	var caps []Capability
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var doc struct {
			Rows []struct {
				Mode           string  `json:"mode"`
				ResidentLayers int     `json:"resident_layers"`
				Batch          int     `json:"batch"`
				DecodeDriverMB float64 `json:"decode_driver_mb"`
				TokensPerS     float64 `json:"tokens_per_s"`
				StepMs         float64 `json:"step_ms"`
			} `json:"rows"`
			Results []struct {
				OK             bool    `json:"ok"`
				Config         string  `json:"config"`
				BudgetMB       float64 `json:"budget_mb"`
				Mode           string  `json:"mode"`
				ResidentLayers int     `json:"resident_layers"`
				Batch          int     `json:"batch"`
				DriverMB       float64 `json:"driver_mb"`
				TokensPerS     float64 `json:"tokens_per_s"`
				StepMs         float64 `json:"step_ms"`
			} `json:"results"`
			Points []struct {
				Mode           string  `json:"mode"`
				ResidentLayers int     `json:"resident_layers"`
				TPOTMs         float64 `json:"tpot_ms"`
				Decode         struct {
					DriverUsedMB float64 `json:"driver_used_mb"`
				} `json:"decode"`
			} `json:"points"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		for _, row := range doc.Rows {
			b := row.Batch
			if b == 0 {
				b = 1
			}
			caps = append(caps, Capability{
				Mode:           row.Mode,
				ResidentLayers: row.ResidentLayers,
				Batch:          b,
				DriverMB:       row.DecodeDriverMB,
				TokensPerS:     row.TokensPerS,
				StepMs:         row.StepMs,
			})
		}
		type rec struct {
			mode, config                   string
			residentLayers, batch          int
			minBudget, driver, tps, stepMs float64
		}
		observed := map[string]rec{}
		for _, row := range doc.Results {
			if !row.OK {
				continue
			}
			prev, ok := observed[row.Config]
			if !ok || row.BudgetMB < prev.minBudget {
				b := row.Batch
				if b == 0 {
					b = 1
				}
				observed[row.Config] = rec{
					mode: row.Mode, config: row.Config,
					residentLayers: row.ResidentLayers, batch: b,
					minBudget: row.BudgetMB, driver: row.DriverMB,
					tps: row.TokensPerS, stepMs: row.StepMs,
				}
			} else if row.TokensPerS > prev.tps {
				prev.tps = row.TokensPerS
				observed[row.Config] = prev
			}
		}
		for _, r := range observed {
			mb := r.minBudget
			caps = append(caps, Capability{
				Mode: r.mode, ResidentLayers: r.residentLayers, Batch: r.batch,
				DriverMB: r.driver, TokensPerS: r.tps, StepMs: r.stepMs, MinBudgetMB: &mb,
			})
		}
		for _, pt := range doc.Points {
			mode := "streamed"
			if pt.Mode == "pinned" {
				mode = "pinned"
			}
			step := pt.TPOTMs
			tps := 0.0
			if step > 0 {
				tps = math.Round((1000.0/step)*1000) / 1000
			}
			caps = append(caps, Capability{
				Mode: mode, ResidentLayers: pt.ResidentLayers, Batch: 1,
				DriverMB: pt.Decode.DriverUsedMB, TokensPerS: tps, StepMs: step,
			})
		}
	}
	return caps, nil
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
