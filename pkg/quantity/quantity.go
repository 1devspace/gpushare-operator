package quantity

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	qtyRE = regexp.MustCompile(`^(\d+)(Mi|Gi)$`)
	msRE  = regexp.MustCompile(`^(\d+)ms$`)
)

// ParseMB reads a CR memoryBudget ("1400Mi" / "2Gi") as driver-visible megabytes.
func ParseMB(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	m := qtyRE.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("unsupported quantity %q", value)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	if m[2] == "Gi" {
		return n * 1024, nil
	}
	return n, nil
}

// ParseMS reads a CR SLO ("80ms"). Empty is nil.
func ParseMS(value string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	m := msRE.FindStringSubmatch(value)
	if m == nil {
		return nil, fmt.Errorf("unsupported duration %q", value)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
