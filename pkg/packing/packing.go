// Package packing sizes elastic layer groups from a measured per-layer cost.
package packing

import (
	"fmt"
	"math"
)

// LayerCost is the measured GPU cost of the always-resident set plus one decoder layer.
type LayerCost struct {
	NumLayers        int
	ProcessFloorMB   float64
	AlwaysResidentMB float64
	LayerMB          float64
	Source           string
}

func (c LayerCost) FootprintMB(residentLayers int) float64 {
	r := residentLayers
	if r < 1 {
		r = 1
	}
	if r > c.NumLayers {
		r = c.NumLayers
	}
	return c.AlwaysResidentMB + float64(r)*c.LayerMB
}

// Pack is the largest elastic group that fits a budget.
type Pack struct {
	Feasible            bool
	Reason              string
	ResidentLayers      int
	NumGroups           int
	PredictedMB         float64
	PredictedTokensPerS float64
	PredictedStepMs     float64
	Pin                 bool
	HeadroomMB          float64
}

func Derive(numLayers int, elasticMB, residentMB, processFloorMB float64, alwaysResidentMB, measuredLayerMB *float64) LayerCost {
	n := numLayers
	if n < 1 {
		n = 1
	}
	span := n - 1
	if span < 1 {
		span = 1
	}
	derivedLayer := math.Max(0, (residentMB-elasticMB)/float64(span))
	layer := derivedLayer
	measured := 0.0
	if measuredLayerMB != nil {
		measured = *measuredLayerMB
	}
	if measured > 1 {
		layer = math.Max(layer, measured)
	}
	if layer < 0.5 {
		layer = 0.5
	}
	var always float64
	if alwaysResidentMB != nil && *alwaysResidentMB > 0 {
		always = *alwaysResidentMB
	} else {
		always = elasticMB - layer
	}
	always = math.Max(always, processFloorMB)
	if always >= elasticMB {
		always = math.Max(processFloorMB, elasticMB-layer)
	}
	source := "derived"
	if measured > 1 {
		source = "measured"
	}
	return LayerCost{
		NumLayers:        n,
		ProcessFloorMB:   processFloorMB,
		AlwaysResidentMB: always,
		LayerMB:          layer,
		Source:           source,
	}
}

func EstimateTokensPerS(elasticTPS, residentTPS float64, numLayers, residentLayers int) float64 {
	n := numLayers
	if n < 1 {
		n = 1
	}
	r := residentLayers
	if r < 1 {
		r = 1
	}
	if r > n {
		r = n
	}
	groups := int(math.Ceil(float64(n) / float64(r)))
	if n == 1 || groups >= n {
		return elasticTPS
	}
	if groups <= 1 {
		return residentTPS
	}
	frac := float64(n-groups) / float64(n-1)
	return elasticTPS + (residentTPS-elasticTPS)*frac
}

func Layers(cost LayerCost, budgetMB, kvReserve, safetyMB, elasticTPS, residentTPS float64) Pack {
	if safetyMB == 0 {
		safetyMB = 32
	}
	usable := budgetMB - kvReserve - safetyMB
	minMB := cost.FootprintMB(1)
	if usable < minMB {
		return Pack{
			Reason: fmt.Sprintf("nothing fits %.0f MB usable; elastic r1 needs %.0f MB", usable, minMB),
		}
	}
	var r int
	if cost.LayerMB <= 0 {
		r = 1
	} else {
		r = int((usable - cost.AlwaysResidentMB) / cost.LayerMB)
	}
	if r < 1 {
		r = 1
	}
	if r > cost.NumLayers {
		r = cost.NumLayers
	}
	for r > 1 && cost.FootprintMB(r) > usable {
		r--
	}
	if cost.FootprintMB(r) > usable {
		return Pack{
			Reason: fmt.Sprintf("nothing fits %.0f MB usable; elastic r1 needs %.0f MB", usable, minMB),
		}
	}
	pred := cost.FootprintMB(r)
	groups := int(math.Ceil(float64(cost.NumLayers) / float64(r)))
	tps := EstimateTokensPerS(elasticTPS, residentTPS, cost.NumLayers, r)
	pin := r >= cost.NumLayers
	step := 0.0
	if tps > 0 {
		step = 1000.0 / tps
	}
	reason := fmt.Sprintf("elastic r%d (%d groups) is the largest pack that fits %.0f MB usable", r, groups, usable)
	if pin {
		reason = fmt.Sprintf("budget holds all %d layers; pin instead of streaming one group", cost.NumLayers)
	}
	return Pack{
		Feasible:            true,
		Reason:              reason,
		ResidentLayers:      r,
		NumGroups:           groups,
		PredictedMB:         round(pred, 2),
		PredictedTokensPerS: round(tps, 2),
		PredictedStepMs:     round(step, 3),
		Pin:                 pin,
		HeadroomMB:          round(usable-pred, 2),
	}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
