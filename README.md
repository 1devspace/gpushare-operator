# gpushare-elastic

Kubernetes can already **count** GPU memory. It cannot decide **how** an LLM
should occupy the bytes it was granted. An engine given half a GPU does not
serve at half scale. It fails to start. In practice, “share the GPU” still
means one workload gets the device.

This repo is the operator that closes that gap.

The user writes an **`ElasticModel`**: a model id, a **memory budget**, and an
optional SLO. They never set `mode`. The first time this GPU sees that model,
the operator profiles it, then assigns one of three occupancy modes:

| Mode | What stays on the GPU | When |
|---|---|---|
| **resident** | Every admitted model, fully | The GPU can hold **all** of them at once. Fastest. |
| **swap** | One full model; the others wait in host RAM | The GPU can hold **one** full model, not two. |
| **elastic** | A slice of layers; the rest stream per token | Even **one** full model does not fit. Smallest footprint. |

Elastic is not faster than swap. It is the mode that still serves when the
others OOM. The worker is the authority: a shape that does not fit is refused
and the object becomes `Degraded`, not `OOMKilled`.

This binary does not load weights or talk to the GPU. The Python profiler and
worker live in **GPUShare-LLM** (clone it next to this tree as
`../GPUShare-LLM`). This repo owns the API, the reconcile loop, and
`status.assigned`.

```
User applies ElasticModel  (model + memoryBudget + optional SLO)
        ↓
First time on this GPU → profiler Job writes a ConfigMap
        ↓
Operator assigns resident / swap / elastic
        ↓
Worker applies it  →  Ready   or   refuse → Degraded
```

**Install on a cluster (CRD, RBAC, first ElasticModel, read the mode):**
[docs/guide.md](docs/guide.md)

**Samples:** [config/samples/four-models.yaml](config/samples/four-models.yaml)
(budget and SLO only — no `mode`).

**KubeCon EU 2027:**
- [docs/cfp-kubecon-eu-2027.md](docs/cfp-kubecon-eu-2027.md) — Sessionize paste pack
- [docs/kubecon-submission.md](docs/kubecon-submission.md) — measured tables

---

## Layout

```
gpushare-elastic/
├── cmd/controller/         Binary: cluster loop or --from-dir
├── api/v1alpha1/           ElasticModel Go types
├── pkg/                    Reconcile, profile, cluster client, operands
├── config/                 CRD, RBAC, profiler SA, sample CRs
├── testdata/profiles/      Checked-in tables for offline runs
├── docs/                   Install guide, KubeCon CFP, measured writeup
└── e2e/                    envtest, live GPU proof, k3s script
```

| Path | Responsibility |
|---|---|
| `cmd/controller/` | `main`: flags, then `operator.Run` |
| `api/v1alpha1/` | CR spec/status: budget in, `assigned.mode` out |
| `pkg/operator/` | No profile → ensure Job + `Profiling`; ConfigMap → choose mode |
| `pkg/reconcile/` | Policy + packing over the current set of objects |
| `pkg/operands/profiler/` | Job / ConfigMap names and pod spec |
| `pkg/cluster/` | List CRs, create Job, write ConfigMap, patch status |
| `pkg/profile/` | Parse the measured JSON the Job publishes |
| `pkg/policy/`, `pkg/modes/`, `pkg/packing/` | Same decisions as GPUShare-LLM `poc/llc` |
| `pkg/worker/` | POST `/admin/config` to `serve.py`; honor refusal → `Degraded` |
| `pkg/host/`, `pkg/quantity/`, `pkg/manifest/` | Placement helpers, k8s quantities, YAML load |
| `config/crd/` | `ElasticModel` CRD |
| `config/rbac/` | Namespace, ServiceAccount, RBAC, optional Deployment |
| `config/operands/` | Profiler ServiceAccount |
| `config/samples/` | Example `ElasticModel`s (no `mode`) |
| `testdata/profiles/` | Offline `--profile-dir` tables |
| `docs/guide.md` | Install CRD + operator, apply a CR, read `status.assigned` |
| `docs/` | KubeCon CFP fields + measured results |
| `e2e/` | API-only test, `LLC_LIVE_PROOF` GPU test, `cluster_proof.sh` |
