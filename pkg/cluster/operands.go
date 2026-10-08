package cluster

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gpushare-elastic/pkg/operands/profiler"
	"gpushare-elastic/pkg/profile"
)

func (c *Client) LoadProfiles(ctx context.Context, namespace string, modelIDs []string) (map[string]profile.Doc, error) {
	out := map[string]profile.Doc{}
	if c.kube == nil {
		return out, nil
	}
	seen := map[string]bool{}
	for _, id := range modelIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cm, err := c.kube.CoreV1().ConfigMaps(namespace).Get(ctx, profiler.ConfigMapName(id), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		doc, err := profiler.DocFromConfigMap(cm)
		if err != nil || doc == nil || !doc.OK() {
			continue
		}
		// Never adopt a profile that does not name this exact model; an
		// empty doc.Model is not trusted either (names are hashed, but a
		// handwritten ConfigMap could still be wrong).
		if doc.Model != id {
			continue
		}
		out[id] = *doc
	}
	return out, nil
}

func (c *Client) EnsureProfilerJob(ctx context.Context, namespace, modelID string, spec profiler.Spec) (profiler.JobStatus, error) {
	if c.kube == nil {
		return profiler.JobStatus{}, nil
	}
	want := profiler.BuildJob(namespace, modelID, spec)
	got, err := c.kube.BatchV1().Jobs(namespace).Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, err := c.kube.BatchV1().Jobs(namespace).Create(ctx, want, metav1.CreateOptions{})
		if err != nil {
			return profiler.JobStatus{}, err
		}
		return profiler.StatusOf(created), nil
	}
	if err != nil {
		return profiler.JobStatus{}, err
	}
	st := profiler.StatusOf(got)
	if st.FailedCompletely() {
		// Retry automatically: remove the failed Job now; the next reconcile
		// recreates it (each attempt still bounded by the Job's backoffLimit).
		prop := metav1.DeletePropagationBackground
		if derr := c.kube.BatchV1().Jobs(namespace).Delete(ctx, want.Name,
			metav1.DeleteOptions{PropagationPolicy: &prop}); derr != nil && !apierrors.IsNotFound(derr) {
			return st, derr
		}
		return profiler.JobStatus{Name: want.Name}, nil
	}
	return st, nil
}

func (c *Client) WriteProfileConfigMap(ctx context.Context, namespace, modelID string, doc profile.Doc) error {
	raw, err := profileJSON(doc)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        profiler.ConfigMapName(modelID),
			Namespace:   namespace,
			Labels:      profiler.Labels(modelID),
			Annotations: map[string]string{profiler.AnnModel: modelID},
		},
		Data: map[string]string{profiler.ProfileKey: raw},
	}
	_, err = c.kube.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = c.kube.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

func profileJSON(doc profile.Doc) (string, error) {
	raw, err := marshalDoc(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
