package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
)

func TestPushAdopted(t *testing.T) {
	var live Config
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&live)
		}
		_ = json.NewEncoder(w).Encode(live)
	}))
	defer srv.Close()
	err := Push(srv.URL, "chat", v1alpha1.Assigned{Mode: "elastic", ResidentLayers: 6, Batch: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPushRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Config{
			Mode: "elastic", ResidentLayers: 1, Batch: 1,
			LastRejection: &Reject{Error: "OutOfMemoryError: reshape failed", Config: Config{Mode: "resident"}},
		})
	}))
	defer srv.Close()
	err := Push(srv.URL, "chat", v1alpha1.Assigned{Mode: "resident", ResidentLayers: 24, Batch: 1}, 0)
	if err == nil {
		t.Fatal("expected rejection")
	}
}

func TestPushSkipped(t *testing.T) {
	if err := Push("", "chat", v1alpha1.Assigned{Mode: "elastic"}, 0); err != nil {
		t.Fatal(err)
	}
}
