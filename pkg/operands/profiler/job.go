package profiler

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gpushare-elastic/pkg/profile"
)

// Spec is how the operator launches the profiler operand.
type Spec struct {
	Image          string
	ServiceAccount string
	WorkspacePVC   string
	HFCachePVC     string
	GPUDevices     bool
	Stub           bool
	StubProfile    string
	NodeSelector   map[string]string
	Command        []string
}

type JobStatus struct {
	Name      string
	Exists    bool
	Active    int32
	Succeeded int32
	Failed    int32
}

func (s JobStatus) FailedCompletely() bool {
	return s.Exists && s.Failed > 0 && s.Succeeded == 0 && s.Active == 0
}

func StatusOf(job *batchv1.Job) JobStatus {
	if job == nil {
		return JobStatus{}
	}
	return JobStatus{
		Name:      job.Name,
		Exists:    true,
		Active:    job.Status.Active,
		Succeeded: job.Status.Succeeded,
		Failed:    job.Status.Failed,
	}
}

func DocFromConfigMap(cm *corev1.ConfigMap) (*profile.Doc, error) {
	if cm == nil {
		return nil, nil
	}
	raw, ok := cm.Data[ProfileKey]
	if !ok || raw == "" {
		return nil, nil
	}
	return profile.FromBytes([]byte(raw))
}

func BuildJob(namespace, modelID string, spec Spec) *batchv1.Job {
	if spec.ServiceAccount == "" {
		spec.ServiceAccount = ServiceAccount
	}
	if spec.Image == "" {
		if spec.Stub {
			spec.Image = "python:3.12-slim"
		} else {
			spec.Image = "rocm/vllm:rocm7.13.0_gfx1151_ubuntu24.04_py3.13_pytorch_2.10.0_vllm_0.19.1"
		}
	}
	backoff := int32(2)
	ttl := int32(86400)
	labels := Labels(modelID)
	cmName := ConfigMapName(modelID)

	env := []corev1.EnvVar{
		{Name: "MODEL", Value: modelID},
		{Name: "NAMESPACE", Value: namespace},
		{Name: "CONFIGMAP_NAME", Value: cmName},
		{Name: "PYTHONUNBUFFERED", Value: "1"},
	}
	if spec.Stub {
		env = append(env, corev1.EnvVar{Name: "PROFILE_STUB", Value: "1"})
		if spec.StubProfile != "" {
			env = append(env, corev1.EnvVar{Name: "STUB_PROFILE_JSON", Value: spec.StubProfile})
		}
	}
	if spec.HFCachePVC != "" {
		env = append(env, corev1.EnvVar{Name: "HF_HOME", Value: "/root/.cache/huggingface"})
	}

	cmd, args := jobCommand(spec)
	priv := spec.GPUDevices && !spec.Stub
	container := corev1.Container{
		Name:            Component,
		Image:           spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         cmd,
		Args:            args,
		Env:             env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if priv {
		yes := true
		container.SecurityContext = &corev1.SecurityContext{Privileged: &yes}
	}

	pod := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: spec.ServiceAccount,
		NodeSelector:       spec.NodeSelector,
		Containers:         []corev1.Container{container},
		Tolerations: []corev1.Toleration{
			{Key: "amd.com/gpu", Operator: corev1.TolerationOpExists},
			{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists},
		},
	}
	if priv {
		pod.HostIPC = true
		gid := int64(992)
		pod.SecurityContext = &corev1.PodSecurityContext{SupplementalGroups: []int64{gid}}
	}
	mountGPU(&pod, spec)
	mountPVCs(&pod, spec)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        JobName(modelID),
			Namespace:   namespace,
			Labels:      labels,
			Annotations: map[string]string{AnnModel: modelID},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}
}

func jobCommand(spec Spec) ([]string, []string) {
	if len(spec.Command) > 0 {
		return spec.Command, nil
	}
	if spec.WorkspacePVC != "" {
		return []string{"python3"}, []string{"/workspace/poc/bin/profile_job.py"}
	}
	// Self-contained stub publish so the operand still works without the repo PVC.
	return []string{"python3", "-c"}, []string{inlinePublish}
}

func mountGPU(pod *corev1.PodSpec, spec Spec) {
	if spec.Stub || !spec.GPUDevices {
		return
	}
	devices := []struct{ name, path string }{
		{"kfd", "/dev/kfd"},
		{"dri-card0", "/dev/dri/card0"},
		{"dri-render", "/dev/dri/renderD128"},
	}
	c := &pod.Containers[0]
	for _, d := range devices {
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: d.name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: d.path,
					Type: hostPathType(corev1.HostPathCharDev),
				},
			},
		})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: d.name, MountPath: d.path})
	}
	pod.Volumes = append(pod.Volumes, corev1.Volume{
		Name: "sysfs",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/sys",
				Type: hostPathType(corev1.HostPathDirectory),
			},
		},
	})
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "sysfs", MountPath: "/sys", ReadOnly: true})
}

func mountPVCs(pod *corev1.PodSpec, spec Spec) {
	c := &pod.Containers[0]
	if spec.WorkspacePVC != "" {
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: spec.WorkspacePVC},
			},
		})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace"})
		c.Env = append(c.Env, corev1.EnvVar{Name: "PYTHONPATH", Value: "/workspace/poc"})
	}
	if spec.HFCachePVC != "" {
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: "hf",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: spec.HFCachePVC},
			},
		})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "hf", MountPath: "/root/.cache/huggingface"})
	}
}

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }

// inlinePublish writes the stub profile ConfigMap using the in-cluster token.
const inlinePublish = `
import json, os, ssl, urllib.error, urllib.request
from pathlib import Path
model = os.environ["MODEL"]
ns = os.environ.get("NAMESPACE") or Path("/var/run/secrets/kubernetes.io/serviceaccount/namespace").read_text().strip()
name = os.environ["CONFIGMAP_NAME"]
raw = os.environ.get("STUB_PROFILE_JSON")
if raw:
    doc = json.loads(raw)
else:
    doc = {
        "model": model, "source": "stub",
        "process_floor_mb": 250, "num_layers": 24,
        "layer_cost": {"num_layers": 24, "process_floor_mb": 250,
                       "always_resident_mb": 400, "layer_mb": 30, "source": "stub"},
        "elastic": {"driver_mb": 680, "tokens_per_s": 150, "ttft_ms": 46, "step_ms": 6, "resident_layers": 1, "batch": 1},
        "resident": {"driver_mb": 1391, "tokens_per_s": 650, "ttft_ms": 12, "step_ms": 1.5, "resident_layers": 24, "batch": 1},
        "swap": {"driver_mb": 1391, "tokens_per_s": 650, "ttft_ms": 12, "switch_ms": 80, "batch": 1},
    }
token = Path("/var/run/secrets/kubernetes.io/serviceaccount/token").read_text()
host = os.environ["KUBERNETES_SERVICE_HOST"]
port = os.environ["KUBERNETES_SERVICE_PORT"]
url = f"https://{host}:{port}/api/v1/namespaces/{ns}/configmaps"
body = json.dumps({
    "apiVersion": "v1", "kind": "ConfigMap",
    "metadata": {
        "name": name, "namespace": ns,
        "labels": {"app.kubernetes.io/managed-by": "elastic-operator",
                   "app.kubernetes.io/component": "profiler"},
        "annotations": {"inference.gpushare.io/model": model},
    },
    "data": {"profile.json": json.dumps(doc)},
}).encode()
ctx = ssl.create_default_context()
ctx.load_verify_locations("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
req = urllib.request.Request(url, data=body, method="POST",
    headers={"Authorization": "Bearer "+token, "Content-Type": "application/json"})
try:
    urllib.request.urlopen(req, context=ctx)
except urllib.error.HTTPError as e:
    if e.code != 409:
        raise
    req = urllib.request.Request(url+"/"+name, data=body, method="PUT",
        headers={"Authorization": "Bearer "+token, "Content-Type": "application/json"})
    urllib.request.urlopen(req, context=ctx)
print("wrote", name)
`
