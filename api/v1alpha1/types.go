// Package v1alpha1 is the ElasticModel API (inference.gpushare.io).
package v1alpha1

const (
	Group   = "inference.gpushare.io"
	Version = "v1alpha1"
	Kind    = "ElasticModel"
	Plural  = "elasticmodels"
	API     = Group + "/" + Version
)

type Workload struct {
	Concurrency          int `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	ExpectedOutputTokens int `json:"expectedOutputTokens,omitempty" yaml:"expectedOutputTokens,omitempty"`
	RequestsPerSwitch    int `json:"requestsPerSwitch,omitempty" yaml:"requestsPerSwitch,omitempty"`
}

type Spec struct {
	Model            string   `json:"model" yaml:"model"`
	MemoryBudget     string   `json:"memoryBudget" yaml:"memoryBudget"`
	LatencySLO       string   `json:"latencySLO,omitempty" yaml:"latencySLO,omitempty"`
	MinThroughput    *float64 `json:"minThroughput,omitempty" yaml:"minThroughput,omitempty"`
	MaxSessions      int      `json:"maxSessions,omitempty" yaml:"maxSessions,omitempty"`
	MaxContextTokens int      `json:"maxContextTokens,omitempty" yaml:"maxContextTokens,omitempty"`
	Priority         int      `json:"priority,omitempty" yaml:"priority,omitempty"`
	TTFTSLO          string   `json:"ttftSLO,omitempty" yaml:"ttftSLO,omitempty"`
	Workload         Workload `json:"workload,omitempty" yaml:"workload,omitempty"`
}

type Assigned struct {
	Mode                     string  `json:"mode,omitempty" yaml:"mode,omitempty"`
	ResidentLayers           int     `json:"residentLayers,omitempty" yaml:"residentLayers,omitempty"`
	Batch                    int     `json:"batch,omitempty" yaml:"batch,omitempty"`
	PredictedDriverMB        float64 `json:"predictedDriverMB,omitempty" yaml:"predictedDriverMB,omitempty"`
	PredictedTokensPerSecond float64 `json:"predictedTokensPerSecond,omitempty" yaml:"predictedTokensPerSecond,omitempty"`
	PredictedStepMs          float64 `json:"predictedStepMs,omitempty" yaml:"predictedStepMs,omitempty"`
	KVReserveMB              float64 `json:"kvReserveMB,omitempty" yaml:"kvReserveMB,omitempty"`
}

type Condition struct {
	Type               string `json:"type" yaml:"type"`
	Status             string `json:"status" yaml:"status"`
	Reason             string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Message            string `json:"message,omitempty" yaml:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty" yaml:"lastTransitionTime,omitempty"`
}

type Status struct {
	Phase              string      `json:"phase,omitempty" yaml:"phase,omitempty"`
	Reason             string      `json:"reason,omitempty" yaml:"reason,omitempty"`
	ObservedGeneration int64       `json:"observedGeneration,omitempty" yaml:"observedGeneration,omitempty"`
	Assigned           Assigned    `json:"assigned,omitempty" yaml:"assigned,omitempty"`
	Conditions         []Condition `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

type ObjectMeta struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
}

type ElasticModel struct {
	APIVersion string     `json:"apiVersion" yaml:"apiVersion"`
	Kind       string     `json:"kind" yaml:"kind"`
	Metadata   ObjectMeta `json:"metadata" yaml:"metadata"`
	Spec       Spec       `json:"spec" yaml:"spec"`
	Status     Status     `json:"status,omitempty" yaml:"status,omitempty"`
}

func (m *ElasticModel) Sessions() int {
	if m.Spec.MaxSessions < 1 {
		return 1
	}
	return m.Spec.MaxSessions
}

func (m *ElasticModel) ContextTokens() int {
	if m.Spec.MaxContextTokens < 1 {
		return 2048
	}
	return m.Spec.MaxContextTokens
}
