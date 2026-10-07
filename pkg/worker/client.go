// Package worker talks to the serving process. The worker is the authority
// on whether a shape actually fits; the operator only predicts.
package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
)

type Config struct {
	Name           string  `json:"name,omitempty"`
	Mode           string  `json:"mode"`
	ResidentLayers int     `json:"residentLayers"`
	Batch          int     `json:"batch"`
	HeadResident   bool    `json:"headResident,omitempty"`
	LastRejection  *Reject `json:"lastRejection,omitempty"`
}

type Reject struct {
	Error  string `json:"error"`
	Config Config `json:"config"`
}

// Push posts an assignment and waits until the worker is running it, or
// returns the worker's own rejection. Empty workerURL skips the call.
func Push(workerURL, name string, assigned v1alpha1.Assigned, timeout time.Duration) error {
	if workerURL == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := trimSlash(workerURL)
	body, err := json.Marshal(map[string]any{
		"name":                     name,
		"mode":                     assigned.Mode,
		"residentLayers":           assigned.ResidentLayers,
		"batch":                    assigned.Batch,
		"predictedDriverMB":        assigned.PredictedDriverMB,
		"predictedTokensPerSecond": assigned.PredictedTokensPerSecond,
		"predictedStepMs":          assigned.PredictedStepMs,
		"kvReserveMB":              assigned.KVReserveMB,
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(base+"/admin/config", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("worker unreachable: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cur, err := Get(base)
		if err != nil {
			return fmt.Errorf("worker unreachable: %w", err)
		}
		if cur.Mode == assigned.Mode && cur.Batch == assigned.Batch && cur.ResidentLayers == assigned.ResidentLayers {
			return nil
		}
		if cur.LastRejection != nil && cur.LastRejection.Config.Mode == assigned.Mode {
			if cur.LastRejection.Error == "" {
				return fmt.Errorf("worker rejected the assignment")
			}
			return fmt.Errorf("%s", cur.LastRejection.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("worker did not adopt the assignment within %s", timeout)
}

func Get(workerURL string) (Config, error) {
	var cur Config
	resp, err := http.Get(trimSlash(workerURL) + "/admin/config")
	if err != nil {
		return cur, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&cur)
	return cur, err
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
