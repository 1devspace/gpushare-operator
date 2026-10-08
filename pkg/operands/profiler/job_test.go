package profiler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"gpushare-elastic/pkg/profile"
)

func TestNamesAreDNS1123(t *testing.T) {
	id := "Qwen/Qwen2.5-0.5B-Instruct"
	job := JobName(id)
	cm := ConfigMapName(id)
	// Suffix = first 8 hex of sha256 of the full model id: collisions between
	// long or similarly-slugged ids are impossible.
	if job != "profile-qwen-qwen2-5-0-5b-instruct-4b05be1d" {
		t.Fatalf("job %s", job)
	}
	if cm != "elastic-profile-qwen-qwen2-5-0-5b-instruct-4b05be1d" {
		t.Fatalf("cm %s", cm)
	}
	if len(job) > 63 || len(cm) > 63 {
		t.Fatalf("too long job=%d cm=%d", len(job), len(cm))
	}
}

func TestBuildJobIsOperandNotOperator(t *testing.T) {
	j := BuildJob("llc", "Qwen/Qwen2.5-0.5B-Instruct", Spec{Stub: true, GPUDevices: false})
	if j.Name != JobName("Qwen/Qwen2.5-0.5B-Instruct") {
		t.Fatalf("name %s", j.Name)
	}
	if j.Labels[LabelManaged] != ManagedBy || j.Labels[LabelComponent] != Component {
		t.Fatalf("labels %+v", j.Labels)
	}
	c := j.Spec.Template.Spec.Containers[0]
	if c.Name != Component {
		t.Fatalf("container %s", c.Name)
	}
	got := map[string]string{}
	for _, e := range c.Env {
		got[e.Name] = e.Value
	}
	if got["MODEL"] != "Qwen/Qwen2.5-0.5B-Instruct" {
		t.Fatalf("MODEL %s", got["MODEL"])
	}
	if got["PROFILE_STUB"] != "1" {
		t.Fatal("stub job must not require a GPU")
	}
	if got["CONFIGMAP_NAME"] != ConfigMapName("Qwen/Qwen2.5-0.5B-Instruct") {
		t.Fatalf("cm env %s", got["CONFIGMAP_NAME"])
	}
	if j.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("Job must be Once, like a validator")
	}
}

func TestGPUJobMountsDevices(t *testing.T) {
	j := BuildJob("llc", "Qwen/Qwen2.5-0.5B-Instruct", Spec{GPUDevices: true, WorkspacePVC: "llc-code"})
	paths := map[string]bool{}
	for _, m := range j.Spec.Template.Spec.Containers[0].VolumeMounts {
		paths[m.MountPath] = true
	}
	for _, want := range []string{"/dev/kfd", "/dev/dri/card0", "/workspace"} {
		if !paths[want] {
			t.Fatalf("missing mount %s: %+v", want, paths)
		}
	}
	cmd := j.Spec.Template.Spec.Containers[0].Command
	if len(cmd) < 1 || cmd[0] != "python3" {
		t.Fatalf("command %v", cmd)
	}
}

func TestDocFromConfigMap(t *testing.T) {
	raw := `{"model":"m","elastic":{"driver_mb":10},"resident":{"driver_mb":20}}`
	doc, err := DocFromConfigMap(&corev1.ConfigMap{Data: map[string]string{ProfileKey: raw}})
	if err != nil || doc == nil || doc.Elastic.DriverMB != 10 {
		t.Fatalf("%v %+v", err, doc)
	}
	if _, err := profile.FromBytes([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}
