// Package profiler is the first-seen measurement operand.
//
// The operator never talks to a GPU. Like nvidia-operator-validator, this
// package only describes a Job that runs on a GPU node, plus the ConfigMap
// that Job writes. The controller then reads that ConfigMap.
package profiler

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"

	"gpushare-elastic/pkg/profile"
)

const (
	ManagedBy      = "elastic-operator"
	AppName        = "gpushare-elastic"
	Component      = "profiler"
	LabelManaged   = "app.kubernetes.io/managed-by"
	LabelName      = "app.kubernetes.io/name"
	LabelComponent = "app.kubernetes.io/component"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelSlug      = "inference.gpushare.io/model-slug"
	AnnModel       = "inference.gpushare.io/model"
	ProfileKey     = "profile.json"
	ServiceAccount = "elastic-profiler"
)

var dnsRE = regexp.MustCompile(`[^a-z0-9-]+`)

// ResourceName is a DNS-1123 slug of the model id (Job / ConfigMap suffix).
// A short hash of the FULL model id is appended so two ids that share a long
// prefix (or slug identically) can never share a Job or ConfigMap.
func ResourceName(modelID string) string {
	s := strings.ToLower(profile.Slug(modelID))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = dnsRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		s = "model"
	}
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if s == "" || !unicode.IsLetter(rune(s[0])) {
		s = "m-" + s
	}
	sum := sha256.Sum256([]byte(modelID))
	return s + "-" + hex.EncodeToString(sum[:])[:8]
}

func JobName(modelID string) string {
	return "profile-" + ResourceName(modelID)
}

func ConfigMapName(modelID string) string {
	return "elastic-profile-" + ResourceName(modelID)
}

func Labels(modelID string) map[string]string {
	return map[string]string{
		LabelManaged:   ManagedBy,
		LabelName:      AppName,
		LabelComponent: Component,
		LabelPartOf:    AppName,
		LabelSlug:      ResourceName(modelID),
	}
}
