// Package reconcile is the operator loop: ElasticModel objects → assignments.
package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/modes"
	"gpushare-elastic/pkg/policy"
	"gpushare-elastic/pkg/profile"
	"gpushare-elastic/pkg/quantity"
	"gpushare-elastic/pkg/worker"
)

const DefaultKVBPT = 15872

type Item struct {
	Model      v1alpha1.ElasticModel
	Generation int64
}

type Result struct {
	Name     string          `json:"name"`
	BudgetMB float64         `json:"budget_mb"`
	Status   v1alpha1.Status `json:"status"`
}

type Report struct {
	DeviceBudgetMB float64         `json:"device_budget_mb"`
	DeviceMode     *modes.Decision `json:"device_mode,omitempty"`
	AssignedMB     float64         `json:"assigned_mb"`
	FreeMB         float64         `json:"free_mb"`
	Models         []Result        `json:"models"`
}

type Options struct {
	DeviceBudgetMB float64
	Profiles       map[string]profile.Doc
	Capabilities   []policy.Capability
	ModeProfile    []modes.Point
	WorkerURL      string
	SafetyMB       float64
}

func Run(items []Item, opt Options) Report {
	if opt.SafetyMB == 0 {
		opt.SafetyMB = 32
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Model.Spec.Priority > items[j].Model.Spec.Priority
	})

	var docs []profile.Doc
	seen := map[string]bool{}
	for _, it := range items {
		id := it.Model.Spec.Model
		if id == "" || seen[id] {
			continue
		}
		if d, ok := opt.Profiles[id]; ok {
			docs = append(docs, d)
			seen[id] = true
		}
	}
	composed := profile.ComposeDevice(docs)
	merged := append(append([]modes.Point{}, opt.ModeProfile...), composed...)

	var device *modes.Decision
	if len(merged) > 0 && len(items) > 0 {
		d := deviceMode(merged, items, opt.DeviceBudgetMB, opt.SafetyMB)
		device = &d
	}

	remaining := opt.DeviceBudgetMB
	var report []Result
	for _, it := range items {
		m := it.Model
		budget, err := quantity.ParseMB(m.Spec.MemoryBudget)
		if err != nil {
			report = append(report, rejected(m, it.Generation, budget, err.Error()))
			continue
		}
		slo, err := quantity.ParseMS(m.Spec.LatencySLO)
		if err != nil {
			report = append(report, rejected(m, it.Generation, budget, err.Error()))
			continue
		}
		reserve := policy.KVReserveMB(DefaultKVBPT, m.ContextTokens(), m.Sessions())
		effective := budget
		if remaining < effective {
			effective = remaining
		}
		own, hasOwn := opt.Profiles[m.Spec.Model]
		pool := opt.Capabilities
		if hasOwn {
			pool = profile.Capabilities(own)
		}
		decision := policy.Choose(pool, effective, slo, m.Spec.MinThroughput, reserve, opt.SafetyMB)

		var deviceChosen string
		if device != nil && device.Feasible {
			deviceChosen = device.Mode
		}
		willStream := deviceChosen == "elastic" ||
			(deviceChosen == "" && decision.Feasible && decision.Mode == "streamed")

		var packedOK bool
		var packedReason string
		var packedLayers int
		var packedMB, packedTPS, packedStep float64
		var packedPin bool
		if willStream && hasOwn {
			p := profile.Pack(own, effective, reserve, opt.SafetyMB)
			if !p.Feasible {
				packedReason = p.Reason
			} else {
				packedOK = true
				packedReason = p.Reason
				packedLayers = p.ResidentLayers
				packedMB = p.PredictedMB
				packedTPS = p.PredictedTokensPerS
				packedStep = p.PredictedStepMs
				packedPin = p.Pin
			}
		}

		memoryOK := !willStream || !hasOwn || packedOK
		var reject string
		if willStream && hasOwn && !packedOK {
			reject = packedReason
		} else if !decision.Feasible {
			reject = decision.Reason
		}
		if reject != "" || !(decision.Feasible && memoryOK) {
			why := reject
			if why == "" {
				why = decision.Reason
			}
			report = append(report, rejected(m, it.Generation, budget, why))
			continue
		}

		assignedMode := deviceChosen
		if assignedMode == "" {
			if decision.Mode == "streamed" {
				assignedMode = "elastic"
			} else {
				assignedMode = "resident"
			}
		}
		layers := decision.ResidentLayers
		predMB := decision.PredictedDriverMB
		predTPS := decision.PredictedTokensPerS
		predStep := decision.PredictedStepMs
		reason := decision.Reason
		if packedOK {
			layers = packedLayers
			predMB = packedMB
			if packedTPS > 0 {
				predTPS = packedTPS
			}
			if packedStep > 0 {
				predStep = packedStep
			}
			reason = packedReason
			if packedPin {
				assignedMode = "resident"
			} else {
				assignedMode = "elastic"
			}
		}
		batch := decision.Batch
		if batch == 0 {
			batch = 1
		}
		spent := predMB + reserve
		remaining -= spent
		assigned := v1alpha1.Assigned{
			Mode:                     assignedMode,
			ResidentLayers:           layers,
			Batch:                    batch,
			PredictedDriverMB:        predMB,
			PredictedTokensPerSecond: predTPS,
			PredictedStepMs:          predStep,
			KVReserveMB:              round2(reserve),
		}
		if err := worker.Push(opt.WorkerURL, m.Metadata.Name, assigned, 0); err != nil {
			report = append(report, Result{
				Name: m.Metadata.Name, BudgetMB: budget,
				Status: v1alpha1.Status{
					Phase:              "Degraded",
					Reason:             fmt.Sprintf("worker could not adopt %s: %s", assignedMode, err.Error()),
					ObservedGeneration: it.Generation,
					Assigned:           assigned,
				},
			})
			continue
		}
		report = append(report, Result{
			Name: m.Metadata.Name, BudgetMB: budget,
			Status: v1alpha1.Status{
				Phase:              "Ready",
				Reason:             reason,
				ObservedGeneration: it.Generation,
				Assigned:           assigned,
			},
		})
	}

	used := opt.DeviceBudgetMB - remaining
	return Report{
		DeviceBudgetMB: opt.DeviceBudgetMB,
		DeviceMode:     device,
		AssignedMB:     round2(used),
		FreeMB:         round2(remaining),
		Models:         report,
	}
}

func deviceMode(points []modes.Point, items []Item, deviceBudget, safety float64) modes.Decision {
	concurrency, outputTokens, runLength := 1, 16, 1
	var ttfts []float64
	first := true
	for _, it := range items {
		w := it.Model.Spec.Workload
		c := w.Concurrency
		if c < 1 {
			c = 1
		}
		o := w.ExpectedOutputTokens
		if o < 1 {
			o = 16
		}
		r := w.RequestsPerSwitch
		if r < 1 {
			r = 1
		}
		if first {
			concurrency, outputTokens, runLength = c, o, r
			first = false
		} else {
			if c > concurrency {
				concurrency = c
			}
			if o > outputTokens {
				outputTokens = o
			}
			if r < runLength {
				runLength = r
			}
		}
		if ms, err := quantity.ParseMS(it.Model.Spec.TTFTSLO); err == nil && ms != nil {
			ttfts = append(ttfts, *ms)
		}
	}
	var maxTTFT *float64
	if len(ttfts) > 0 {
		m := ttfts[0]
		for _, t := range ttfts[1:] {
			if t < m {
				m = t
			}
		}
		maxTTFT = &m
	}
	return modes.Choose(points, modes.ChooseArgs{
		BudgetMB: deviceBudget, Concurrency: concurrency,
		OutputTokens: outputTokens, RunLength: runLength,
		NumModels: len(items), MaxTTFTMs: maxTTFT, SafetyMB: safety,
	})
}

func rejected(m v1alpha1.ElasticModel, gen int64, budget float64, reason string) Result {
	return Result{
		Name: m.Metadata.Name, BudgetMB: budget,
		Status: v1alpha1.Status{
			Phase: "Rejected", Reason: reason, ObservedGeneration: gen,
		},
	}
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// LoadProfilesFromDir reads cached auto-profiles (no GPU measure).
func LoadProfilesFromDir(autoDir string, modelIDs []string) map[string]profile.Doc {
	out := map[string]profile.Doc{}
	if autoDir == "" {
		return out
	}
	for _, id := range modelIDs {
		d, err := profile.LoadCached(id, autoDir)
		if err != nil || d == nil {
			continue
		}
		out[id] = *d
	}
	entries, err := os.ReadDir(autoDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		d, err := profile.ReadFile(filepath.Join(autoDir, e.Name()))
		if err != nil || d == nil || d.Model == "" {
			continue
		}
		if _, ok := out[d.Model]; !ok {
			out[d.Model] = *d
		}
	}
	return out
}
