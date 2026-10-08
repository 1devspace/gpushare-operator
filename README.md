# gpushare-elastic

Go operator for **GPUShare-LLM** (sibling repo; clone it next to this one as
`../GPUShare-LLM`). The user writes an **`ElasticModel`** (`model` +
`memoryBudget` + optional SLO). They never set `mode`. This binary assigns
**resident / swap / elastic**.

It does not load weights or talk to the GPU. The Python profiler and worker
in GPUShare-LLM do that. This repo watches the API, creates the first-seen
Job, reads the profile ConfigMap, and writes `status.assigned`.

**How to run it:** `guide.md` in the GPUShare-LLM repo.

---

## Layout

```
gpushare-elastic/
├── cmd/controller/         Binary entry: cluster loop or --from-dir
├── api/v1alpha1/           ElasticModel Go types
├── pkg/                    Reconcile, profile, cluster client, operands
├── config/                 CRD, RBAC, profiler SA, sample CRs
├── testdata/profiles/      Checked-in tables for offline runs
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
| `pkg/policy/`, `pkg/modes/`, `pkg/packing/` | Same decisions as `poc/llc` |
| `pkg/worker/` | POST `/admin/config` to `serve.py`; honor refusal → `Degraded` |
| `pkg/host/`, `pkg/quantity/`, `pkg/manifest/` | Placement helpers, k8s quantities, YAML load |
| `config/crd/` | CRD YAML |
| `config/rbac/` | Namespace, ServiceAccount, RBAC, optional Deployment |
| `config/operands/` | Profiler ServiceAccount |
| `config/samples/` | Example `ElasticModel`s (no `mode`) |
| `testdata/profiles/` | Offline `--profile-dir` tables |
| `e2e/` | API-only test, `LLC_LIVE_PROOF` GPU test, `cluster_proof.sh` |
