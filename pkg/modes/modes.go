// Package modes picks resident vs swap vs elastic from a measured profile.
package modes

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

var Names = []string{"resident", "swap", "elastic"}

var aliases = map[string]string{
	"both-resident": "resident",
	"both_resident": "resident",
}

// Point is one measured observation of a mode under a workload.
type Point struct {
	Mode          string
	Concurrency   int
	RunLength     int
	NumModels     int
	MinBudgetMB   float64
	DriverMB      float64
	RawTokensPerS float64
	TTFTMs        float64
	SwitchMs      float64
}

func (p Point) RequiredMB() float64 {
	if p.DriverMB != 0 {
		return p.DriverMB
	}
	return p.MinBudgetMB
}

// Decision is the occupancy mode chosen for a device budget and SLO.
type Decision struct {
	Feasible            bool
	Reason              string
	Mode                string
	PredictedTokensPerS float64
	PredictedTTFTMs     float64
	PredictedDriverMB   float64
	RequiredMB          float64
	HeadroomMB          float64
	Considered          map[string]string
}

func LoadProfile(paths ...string) ([]Point, error) {
	type key struct {
		mode string
		conc int
		rl   int
	}
	best := map[key]Point{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var doc struct {
			Models map[string]any `json:"models"`
			Runs   []struct {
				Feasible      bool    `json:"feasible"`
				Strategy      string  `json:"strategy"`
				Concurrency   int     `json:"concurrency"`
				RunLength     int     `json:"run_length"`
				WallS         float64 `json:"wall_s"`
				SwitchMsTotal float64 `json:"switch_ms_total"`
				OutputTokens  float64 `json:"output_tokens"`
				BudgetMB      float64 `json:"budget_mb"`
				PeakDriverMB  float64 `json:"peak_driver_mb"`
				TTFTMsMean    float64 `json:"ttft_ms_mean"`
				SwitchMsMean  float64 `json:"switch_ms_mean"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		nModels := len(doc.Models)
		if nModels == 0 {
			nModels = 1
		}
		for _, row := range doc.Runs {
			if !row.Feasible {
				continue
			}
			mode := row.Strategy
			if a, ok := aliases[mode]; ok {
				mode = a
			}
			if !isMode(mode) {
				continue
			}
			conc := row.Concurrency
			if conc == 0 {
				conc = 1
			}
			rl := row.RunLength
			if rl == 0 {
				rl = 1
			}
			switchS := row.SwitchMsTotal / 1000.0
			serveS := row.WallS - switchS
			if serveS < 1e-6 {
				serveS = 1e-6
			}
			k := key{mode, conc, rl}
			cand := Point{
				Mode:          mode,
				Concurrency:   conc,
				RunLength:     rl,
				NumModels:     nModels,
				MinBudgetMB:   row.BudgetMB,
				DriverMB:      row.PeakDriverMB,
				RawTokensPerS: row.OutputTokens / serveS,
				TTFTMs:        row.TTFTMsMean,
				SwitchMs:      row.SwitchMsMean,
			}
			prev, ok := best[k]
			if !ok {
				best[k] = cand
				continue
			}
			if cand.MinBudgetMB < prev.MinBudgetMB {
				prev.MinBudgetMB = cand.MinBudgetMB
			}
			if cand.RawTokensPerS > prev.RawTokensPerS {
				prev.RawTokensPerS = cand.RawTokensPerS
				prev.TTFTMs = cand.TTFTMs
				prev.SwitchMs = cand.SwitchMs
				prev.DriverMB = cand.DriverMB
			}
			best[k] = prev
		}
	}
	out := make([]Point, 0, len(best))
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mode != out[j].Mode {
			return out[i].Mode < out[j].Mode
		}
		if out[i].Concurrency != out[j].Concurrency {
			return out[i].Concurrency < out[j].Concurrency
		}
		return out[i].RunLength < out[j].RunLength
	})
	return out, nil
}

func isMode(m string) bool {
	for _, n := range Names {
		if n == m {
			return true
		}
	}
	return false
}

func match(points []Point, mode string, concurrency, runLength int) *Point {
	var best *Point
	bestScore := [2]int{math.MaxInt, math.MaxInt}
	for i := range points {
		p := &points[i]
		if p.Mode != mode {
			continue
		}
		score := [2]int{abs(p.Concurrency - concurrency), abs(p.RunLength - runLength)}
		if best == nil || score[0] < bestScore[0] || (score[0] == bestScore[0] && score[1] < bestScore[1]) {
			best = p
			bestScore = score
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func EffectiveTokensPerS(p Point, tokensPerSwitch float64) float64 {
	if p.SwitchMs <= 0 || tokensPerSwitch <= 0 {
		return p.RawTokensPerS
	}
	serveS := tokensPerSwitch / math.Max(p.RawTokensPerS, 1e-6)
	return tokensPerSwitch / (serveS + p.SwitchMs/1000.0)
}

func ExpectedTTFTMs(p Point, runLength int) float64 {
	if p.SwitchMs <= 0 {
		return p.TTFTMs
	}
	if runLength < 1 {
		runLength = 1
	}
	return p.TTFTMs + p.SwitchMs/float64(runLength)
}

type ChooseArgs struct {
	BudgetMB      float64
	Concurrency   int
	OutputTokens  int
	RunLength     int
	NumModels     int
	MaxTTFTMs     *float64
	MinTokensPerS *float64
	SafetyMB      float64
}

func Choose(points []Point, args ChooseArgs) Decision {
	if args.Concurrency <= 0 {
		args.Concurrency = 1
	}
	if args.OutputTokens <= 0 {
		args.OutputTokens = 16
	}
	if args.RunLength <= 0 {
		args.RunLength = 1
	}
	if args.SafetyMB == 0 {
		args.SafetyMB = 32
	}
	if len(points) == 0 {
		return Decision{Reason: "no measured mode profile"}
	}
	usable := args.BudgetMB - args.SafetyMB
	considered := map[string]string{}
	type viable struct {
		p    Point
		tps  float64
		ttft float64
	}
	var ok []viable
	tokensPerSwitch := math.Max(1, float64(args.RunLength*args.Concurrency*args.OutputTokens))

	for _, mode := range Names {
		p := match(points, mode, args.Concurrency, args.RunLength)
		if p == nil {
			considered[mode] = "not measured"
			continue
		}
		if p.RequiredMB() > usable {
			considered[mode] = fmt.Sprintf("needs %.0f MB, %.0f MB usable", p.RequiredMB(), usable)
			continue
		}
		tps := EffectiveTokensPerS(*p, tokensPerSwitch)
		ttft := ExpectedTTFTMs(*p, args.RunLength)
		if args.MaxTTFTMs != nil && ttft > *args.MaxTTFTMs {
			considered[mode] = fmt.Sprintf(
				"fits, but %.0f ms first token exceeds the %.0f ms target",
				ttft, *args.MaxTTFTMs,
			)
			continue
		}
		if args.MinTokensPerS != nil && tps < *args.MinTokensPerS {
			considered[mode] = fmt.Sprintf(
				"fits, but %.0f tok/s is below the %.0f tok/s target",
				tps, *args.MinTokensPerS,
			)
			continue
		}
		considered[mode] = fmt.Sprintf("viable at %.0f tok/s, %.0f ms first token", tps, ttft)
		ok = append(ok, viable{*p, tps, ttft})
	}

	if len(ok) == 0 {
		cheapest := points[0]
		for _, p := range points[1:] {
			if p.RequiredMB() < cheapest.RequiredMB() {
				cheapest = p
			}
		}
		blocked := true
		for _, why := range considered {
			if !(contains(why, "needs") || contains(why, "not measured")) {
				blocked = false
				break
			}
		}
		reason := "every mode that fits misses the SLO"
		if blocked {
			reason = fmt.Sprintf(
				"nothing fits %.0f MB usable; the lowest mode (%s) needs %.0f MB",
				usable, cheapest.Mode, cheapest.RequiredMB(),
			)
		}
		return Decision{Reason: reason, Considered: considered}
	}

	best := ok[0]
	for _, v := range ok[1:] {
		if v.tps > best.tps || (v.tps == best.tps && v.p.RequiredMB() < best.p.RequiredMB()) {
			best = v
		}
	}
	alts := 0
	for _, v := range ok {
		if v.p.Mode != best.p.Mode {
			alts++
		}
	}
	reason := fmt.Sprintf("%s is the only mode that fits this budget and SLO", best.p.Mode)
	if alts > 0 {
		reason = fmt.Sprintf("%s is the fastest of %d modes that fit this budget", best.p.Mode, len(ok))
	}
	return Decision{
		Feasible:            true,
		Reason:              reason,
		Mode:                best.p.Mode,
		PredictedTokensPerS: round(best.tps, 2),
		PredictedTTFTMs:     round(best.ttft, 1),
		PredictedDriverMB:   round(best.p.DriverMB, 1),
		RequiredMB:          round(best.p.RequiredMB(), 1),
		HeadroomMB:          round(usable-best.p.RequiredMB(), 1),
		Considered:          considered,
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
