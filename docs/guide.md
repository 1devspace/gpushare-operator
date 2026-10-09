# Guide: install the occupancy operator

This repo is the Kubernetes operator. The **`ElasticModel`** API is defined
here (`config/crd/elasticmodel.yaml`, Go types in `api/v1alpha1`). The user
never sets `mode`. They set `model` + `memoryBudget` (+ optional SLO). The
operator writes `status.assigned.mode` (`resident` / `swap` / `elastic`).

This binary does not load weights or talk to the GPU. The Python profiler and
worker live in the sibling **GPUShare-LLM** repo (clone it next to this tree as
`../GPUShare-LLM`). You need that repo only for a real measure and for
inference.

What this repo is for: [README.md](../README.md).

---

## 0. What you need

- `kubectl`, Go 1.22+
- A Kubernetes API with at least one Ready node (k3s, kubeadm, …)
- For a **real** first-seen profile: GPUShare-LLM on a PVC (or run
  `profile_job.py` on the GPU host, step 5b), HuggingFace access, ROCm
- For **API-only** checks: `--profiler-stub` (synthetic table, no GPU)

---

## 1. Build the operator

```bash
go test ./...
go build -o bin/controller ./cmd/controller
```

---

## 2. Start (or use) a cluster

Any cluster with a working kubelet is fine. Example: k3s in Docker:

```bash
docker rm -f k3s-gpushare
docker run -d --name k3s-gpushare \
  --privileged --cgroupns=host \
  --tmpfs /run --tmpfs /var/run \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -p 16443:6443 \
  rancher/k3s:v1.31.6-k3s1 \
  server \
  --tls-san=127.0.0.1 \
  --write-kubeconfig-mode=644 \
  --disable=traefik,metrics-server,servicelb
```

```bash
docker exec k3s-gpushare cat /etc/rancher/k3s/k3s.yaml > /tmp/k3s.yaml
sed -i 's#https://127.0.0.1:6443#https://127.0.0.1:16443#' /tmp/k3s.yaml
export KUBECONFIG=/tmp/k3s.yaml
kubectl get --raw=/readyz
kubectl get nodes
```

You want `ok` from `/readyz` and a `Ready` node so Jobs can schedule.

---

## 3. Install the operator pieces

Everything the API needs is in this repo:

```bash
kubectl apply -f config/crd/elasticmodel.yaml
kubectl apply -f config/rbac/controller.yaml
kubectl apply -f config/operands/profiler.yaml
kubectl wait --for=condition=Established \
  crd/elasticmodels.inference.gpushare.io --timeout=30s
```

This creates:

| Object | File |
|---|---|
| CRD `elasticmodels.inference.gpushare.io` (short name `em`) | `config/crd/elasticmodel.yaml` |
| Namespace `llc` | `config/rbac/controller.yaml` |
| ServiceAccount `elastic-controller` + Role / RoleBinding | same |
| Optional Deployment `elastic-controller` | same |
| ServiceAccount `elastic-profiler` + Role to write ConfigMaps | `config/operands/profiler.yaml` |

The Deployment image is `gpushare-elastic:latest`. Until you build and load
that image, run the host binary with the same kubeconfig (next steps). The
Deployment also passes `--workspace-pvc=llc-code` and `--hf-cache-pvc=llc-hf`;
those claims must exist for in-cluster Jobs to measure. The binary defaults
those flags to empty.

---

## 4. Declare a model (user YAML)

Do **not** put `mode` in the spec. The type forbids it.

```yaml
# /tmp/smol-new.yaml
apiVersion: inference.gpushare.io/v1alpha1
kind: ElasticModel
metadata:
  name: smol-new
  namespace: llc
spec:
  model: HuggingFaceTB/SmolLM2-135M-Instruct
  memoryBudget: 800Mi
  ttftSLO: 80ms
  maxSessions: 1
  maxContextTokens: 512
  priority: 10
```

```bash
kubectl apply -f /tmp/smol-new.yaml
kubectl -n llc get em
# MODE column is empty until a profile exists
```

Ready-made objects: [`config/samples/four-models.yaml`](config/samples/four-models.yaml).

---

## 5. First reconcile — Job + `Profiling`

```bash
./bin/controller \
  --kubeconfig "$KUBECONFIG" \
  --namespace llc \
  --device-budget-mb 800 \
  --interval 15s
```

On the first unseen `spec.model` it:

1. creates a profiler Job (as `elastic-profiler`)
2. sets `status.phase=Profiling`

```bash
kubectl -n llc get em,job,pods
```

Job and ConfigMap names are a DNS slug of the model id plus an 8-hex hash of
the **full** id (so two long prefixes cannot collide). Example:

`HuggingFaceTB/SmolLM2-135M-Instruct` →
`profile-huggingfacetb-smollm2-135m-ins-b68b303b`

Leave `--interval 15s` running, or run the binary again after 5a/5b.

### 5a. Job pod can run (real GPU node)

Pass a PVC that contains GPUShare-LLM so the Job can run `profile_job.py`:

```bash
./bin/controller \
  --kubeconfig "$KUBECONFIG" \
  --namespace llc \
  --device-budget-mb 800 \
  --workspace-pvc=llc-code \
  --hf-cache-pvc=llc-hf \
  --interval 15s
```

Without `--workspace-pvc` and without `--profiler-stub`, the Job has no
measurement script. Use `--profiler-stub` only for a synthetic table.

When the Job finishes it writes ConfigMap `elastic-profile-<slug>-<hash>`
with key `profile.json`.

```bash
kubectl -n llc get cm -l app.kubernetes.io/component=profiler
kubectl -n llc get job -l app.kubernetes.io/component=profiler
```

### 5b. Job pod cannot start

Measure on the GPU host with the same script the Job would run, then store
the file as that ConfigMap. Clone GPUShare-LLM next to this repo.

```bash
export PYTHONPATH=../GPUShare-LLM/poc
python3 ../GPUShare-LLM/poc/bin/profile_job.py \
  --model HuggingFaceTB/SmolLM2-135M-Instruct \
  --force \
  --out /tmp/profile.json

python3 -c "import json; d=json.load(open('/tmp/profile.json')); print(d['source'], d['elastic']['driver_mb'], d['resident']['driver_mb'])"

CM=elastic-profile-huggingfacetb-smollm2-135m-ins-b68b303b
kubectl -n llc create configmap "$CM" \
  --from-file=profile.json=/tmp/profile.json \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n llc annotate cm "$CM" \
  inference.gpushare.io/model=HuggingFaceTB/SmolLM2-135M-Instruct --overwrite
kubectl -n llc label cm "$CM" \
  app.kubernetes.io/component=profiler \
  app.kubernetes.io/managed-by=elastic-operator --overwrite
```

`--force` re-measures this GPU. First run downloads weights into
`~/.cache/huggingface`.

---

## 6. Second reconcile — mode is written

```bash
./bin/controller \
  --kubeconfig "$KUBECONFIG" \
  --namespace llc \
  --device-budget-mb 800
```

```bash
kubectl -n llc get em -o wide
kubectl -n llc get em smol-new -o yaml
```

You should see something like:

```text
NAME       MODEL                                 BUDGET   MODE       LAYERS   PHASE
smol-new   HuggingFaceTB/SmolLM2-135M-Instruct   800Mi    resident   30       Ready
```

`--device-budget-mb` is the **whole GPU** the operator may use. The CR
`memoryBudget` is a cap for that object.

| Phase | Meaning |
|---|---|
| `Profiling` | First-seen; waiting for the profile ConfigMap |
| `Ready` | Mode assigned (and worker adopted it, if `--worker-url` is set) |
| `Rejected` | Nothing fits the budget / SLO |
| `Degraded` | Assigned, but the worker refused (or the profiler Job failed) |

---

## 7. Infer in the assigned shape

The worker is GPUShare-LLM `serve.py`. Read mode from the object, then start
the worker:

```bash
MODE=$(kubectl -n llc get em smol-new -o jsonpath='{.status.assigned.mode}')
LAYERS=$(kubectl -n llc get em smol-new -o jsonpath='{.status.assigned.residentLayers}')
echo "assigned $MODE r$LAYERS"

export PYTHONPATH=../GPUShare-LLM/poc
python3 ../GPUShare-LLM/poc/bin/serve.py \
  --model HuggingFaceTB/SmolLM2-135M-Instruct \
  --port 8000 \
  --mode "$MODE" \
  --resident-layers "$LAYERS" \
  --batch 1 \
  --budget-mb 800
```

```bash
curl -sS http://127.0.0.1:8000/healthz
curl -sS http://127.0.0.1:8000/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"The capital of France is","max_tokens":10}'
```

Point the operator at the worker so it pushes `/admin/config`:

```bash
./bin/controller \
  --kubeconfig "$KUBECONFIG" \
  --namespace llc \
  --device-budget-mb 800 \
  --worker-url http://127.0.0.1:8000 \
  --interval 15s
```

If the worker cannot hold the shape it refuses; status becomes `Degraded`
instead of a silent OOM.

---

## Offline (no cluster)

Policy only, using files already in this repo:

```bash
./bin/controller \
  --from-dir config/samples \
  --profile-dir testdata/profiles \
  --device-budget-mb 1200 \
  --out /tmp/plan.json
```

Without a profile ConfigMap (and without `--profile-dir`), objects stay
`Profiling` — the operator does not invent a mode.

---

## Tests

```bash
# envtest: CRD + Job object + ConfigMap + mode (no GPU)
go test ./e2e -count=1

# GPU + operator loop (envtest API, real measure + completion)
LLC_LIVE_PROOF=1 go test ./e2e -run TestLiveOperatorProof -count=1 -timeout 20m -v

# real cluster API
export KUBECONFIG=/tmp/k3s.yaml
./e2e/cluster_proof.sh
```

---

## Troubleshooting

| Symptom | What to do |
|---|---|
| `CRD not established` | `kubectl get crd elasticmodels.inference.gpushare.io` |
| Phase stuck `Profiling` | No ConfigMap yet. Check Job logs, or do step 5b |
| Job pod `Pending` | Missing node, GPU, or PVC. Pass `--workspace-pvc` or use 5b |
| `Rejected` + “needs N MB” | Raise `memoryBudget` or `--device-budget-mb` |
| Wrong model table | Names include a hash of the full `spec.model`. Delete the old CM + Job to re-profile |
| Worker `Degraded` | Worker log: assignment did not fit; status keeps the last good serve |
| In-cluster Deployment never starts | No Dockerfile / image `gpushare-elastic:latest`. Run `bin/controller` on the host |

Failed profiler Jobs are deleted so the next reconcile can recreate them.
