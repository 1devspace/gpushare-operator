// Package cluster talks to the Kubernetes API for ElasticModel objects.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	v1alpha1 "gpushare-elastic/api/v1alpha1"
	"gpushare-elastic/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var GVR = schema.GroupVersionResource{
	Group:    v1alpha1.Group,
	Version:  v1alpha1.Version,
	Resource: v1alpha1.Plural,
}

type Client struct {
	dyn  dynamic.Interface
	kube kubernetes.Interface
}

func New(cfg *rest.Config) (*Client, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{dyn: dyn, kube: kube}, nil
}

func (c *Client) Kube() kubernetes.Interface { return c.kube }

func (c *Client) List(ctx context.Context, namespace string) ([]reconcile.Item, error) {
	list, err := c.dyn.Resource(GVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]reconcile.Item, 0, len(list.Items))
	for i := range list.Items {
		it, err := ItemFromUnstructured(&list.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (c *Client) Create(ctx context.Context, m v1alpha1.ElasticModel) error {
	if m.APIVersion == "" {
		m.APIVersion = v1alpha1.API
	}
	if m.Kind == "" {
		m.Kind = v1alpha1.Kind
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var u unstructured.Unstructured
	if err := u.UnmarshalJSON(raw); err != nil {
		return err
	}
	_, err = c.dyn.Resource(GVR).Namespace(m.Metadata.Namespace).Create(ctx, &u, metav1.CreateOptions{})
	return err
}

func (c *Client) Get(ctx context.Context, namespace, name string) (v1alpha1.ElasticModel, error) {
	var m v1alpha1.ElasticModel
	u, err := c.dyn.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return m, err
	}
	raw, err := u.MarshalJSON()
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

func (c *Client) PatchStatuses(ctx context.Context, namespace string, report reconcile.Report) error {
	for _, m := range report.Models {
		body := map[string]any{"status": StatusMap(m.Status)}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = c.dyn.Resource(GVR).Namespace(namespace).Patch(
			ctx, m.Name, types.MergePatchType, raw, metav1.PatchOptions{}, "status",
		)
		if err != nil {
			return fmt.Errorf("status patch %s: %w", m.Name, err)
		}
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, namespace, name string) error {
	return c.dyn.Resource(GVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func ItemFromUnstructured(u *unstructured.Unstructured) (reconcile.Item, error) {
	raw, err := u.MarshalJSON()
	if err != nil {
		return reconcile.Item{}, err
	}
	var m v1alpha1.ElasticModel
	if err := json.Unmarshal(raw, &m); err != nil {
		return reconcile.Item{}, err
	}
	return reconcile.Item{Model: m, Generation: u.GetGeneration()}, nil
}

func StatusMap(s v1alpha1.Status) map[string]any {
	out := map[string]any{
		"phase":              s.Phase,
		"reason":             s.Reason,
		"observedGeneration": s.ObservedGeneration,
	}
	if s.Assigned.Mode != "" {
		out["assigned"] = map[string]any{
			"mode":                     s.Assigned.Mode,
			"residentLayers":           s.Assigned.ResidentLayers,
			"batch":                    s.Assigned.Batch,
			"predictedDriverMB":        s.Assigned.PredictedDriverMB,
			"predictedTokensPerSecond": s.Assigned.PredictedTokensPerSecond,
			"predictedStepMs":          s.Assigned.PredictedStepMs,
			"kvReserveMB":              s.Assigned.KVReserveMB,
		}
	}
	if len(s.Conditions) > 0 {
		conds := make([]any, 0, len(s.Conditions))
		for _, c := range s.Conditions {
			conds = append(conds, map[string]any{
				"type":               c.Type,
				"status":             c.Status,
				"reason":             c.Reason,
				"message":            c.Message,
				"lastTransitionTime": c.LastTransitionTime,
			})
		}
		out["conditions"] = conds
	}
	return out
}
