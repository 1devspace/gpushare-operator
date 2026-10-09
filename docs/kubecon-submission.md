# A policy for how an LLM occupies constrained GPU memory

Sessionize paste pack (title, abstract, poster, checklist):
[cfp-kubecon-eu-2027.md](cfp-kubecon-eu-2027.md).
Deadline: 11 October 2026, 11:59 PM CEST.

Kubernetes can already *account* for GPU memory. It cannot decide **how** an
LLM should occupy the memory it was granted. That choice is not one number
(how many layers to keep resident). It is one of three strategies, and the
right one depends on the budget, the workload, and a measured profile.

| Mode | What stays on the GPU | What it costs |
|---|---|---|
| **resident** | Every co-hosted model, in full | Fastest sustained generation. Needs room for all of them. |
| **swap** | The active model only; the rest stay in host RAM | Nearly as fast as resident. Pays ~50–200 ms per model change. |
| **elastic** | A slice of every model; the rest streams per token | Serves below the memory one full model needs. Slow on sustained decode. Fast to first token. |

Elastic residency does **not** beat swapping for sustained generation. On this
hardware it is about 4× slower whenever swapping fits. What it buys is a
lower floor, and a first-token latency that never includes a switch. The
contribution is therefore a controller that **picks the mode**, not a claim
that one mode is always best.

All numbers below were measured on one Radeon 8060S (gfx1151, 96 GB unified
LPDDR5X, ROCm, PyTorch 2.10) with `Qwen/Qwen2.5-0.5B-Instruct` and
`HuggingFaceTB/SmolLM2-360M-Instruct` in fp16. Every claim has a command that
reproduces it.

---

## The gap

DRA consumable capacity lets several pods share one device on paper. The
Gateway API Inference Extension routes on KV-cache pressure and queue depth.
Neither tells a serving engine what to do when it is granted less memory than
a model's natural footprint. An engine given half a GPU does not serve at
half scale; it fails to start. "Share the GPU" today still means "one of you
gets it."

The missing piece is an engine that can run in more than one occupancy mode,
plus a policy that chooses the mode from a budget and a workload.

## What was built

| Piece | File | What it does |
|---|---|---|
| Memory accounting | `poc/llc/memory.py` | Four counters: torch allocated, torch reserved, driver-visible, host RSS |
| Streaming engine | `poc/llc/engine.py` | Keeps `r` decoder layers resident; prefill and decode share one path |
| Multi-model host | `poc/llc/host.py` | Several models in one process; reshape is atomic and rolls back on OOM |
| Mode policy | `poc/llc/modes.py` | Picks resident / swap / elastic from a measured profile |
| Shape policy | `poc/llc/policy.py` | Inside a mode, picks residency and batch width |
| `ElasticModel` CRD | `k8s/crd-elasticmodel.yaml` | Budget, SLO, and workload as a declarative API |
| Controller | `poc/bin/controller.py` | Reconciles those into a mode; the worker is the authority on whether it fits |
| Serving worker | `poc/bin/serve.py` | OpenAI-compatible, batching, reshapeable at runtime |

---

## Finding 1: the three modes are not interchangeable

Two models, one enforced budget, one interleaved workload. The policy names a
mode from its profile; then all three modes actually run.

| Scenario | Budget | Workload | Policy pick | Hardware best | Elastic tok/s | Swap tok/s | Resident tok/s |
|---|---|---|---|---|---|---|---|
| starved-batch | 1200 MB | conc 8, 16 tok, switch every request | **elastic** | elastic | **151** | OOM | OOM |
| starved-single | 1200 MB | conc 1, 16 tok | **elastic** | elastic | **23** | OOM | OOM |
| sustained-mixed | 1800 MB | conc 8, 16 tok, switch every 4 | **swap** | swap | 165 | **653** | OOM |
| thrashing | 1800 MB | conc 8, 16 tok, switch every request | **swap** | swap | 165 | **522** | OOM |
| roomy | 2600 MB | conc 8, 16 tok, switch every 4 | **resident** | resident | 161 | 667 | **675** |
| ttft-sensitive | 1800 MB | conc 8, 16 tok, TTFT ≤ 55 ms | **elastic** | elastic | **163** | 539 (58.8 ms first token, over SLO) | OOM |

Three facts sit in that table:

1. **Elastic is the only mode that serves below one full model's footprint.**
   At 1200 MB, swap and resident are infeasible. Elastic serves 151 tok/s
   (batch 8) or 23 tok/s (batch 1) in ~740 MB.
2. **Once swapping fits, it is substantially faster.** At 1800 MB, swap is
   4.0× elastic on a mixed workload and 3.2× even when every request
   switches model. The switch itself is 50–90 ms — cheap next to per-token
   streaming.
3. **A first-token SLO can override the throughput ranking.** Swap's raw
   decode is faster, but a request that has to wait for a switch sees ~59 ms
   to first token. Elastic, with nothing to move, meets a 55 ms target that
   swap misses. That is the one measured case where the policy correctly
   chooses the slower mode.

The policy agreed with the hardware in **6 of 6** scenarios. It never selected
elastic when swapping was feasible, faster, and allowed by the SLO. Predicted
throughput was within 4.3% on average, 13% worst.

```
python3 poc/bin/experiment_policy.py     # writes results/policy_modes.json
python3 poc/tests/validate_modes.py      # 24 checks
```

## Finding 2: swapping is a ~100 ms host-RAM move, not a cold start

A request arriving for a model that is not the one just served:

| Decode length | reload from disk | swap from host RAM | elastic (already live) |
|---|---|---|---|
| 1 token | 998 ms | 222 ms | **107 ms** |
| 8 tokens | 1175 ms | **234 ms** | 678 ms |
| 32 tokens | 1517 ms | **670 ms** | 1561 ms |
| 128 tokens | 3192 ms | **2233 ms** | 8470 ms |

Elastic wins time-to-first-token, always, because there is no switch. It only
wins end-to-end at a **single output token**. By 8 tokens, swapping from host
RAM is ahead; by 128 tokens elastic is 3.8× slower. Reloading from disk is
never competitive once weights are warm.

```
python3 poc/bin/experiment_switch.py
```

## Finding 3: elastic's memory advantage has a ceiling

Swapping occupies one model regardless of how many are hosted. Elastic pays a
slice per model.

| Models on the device | elastic | swap | all resident |
|---|---|---|---|
| 1 | 593 MB | 1323 MB | 1323 MB |
| 2 | 853 MB | 1323 MB | 2273 MB |
| 4 | 1373 MB | 1383 MB | 4255 MB |

Elastic costs about 260–280 MB per extra 0.5B-class model and crosses swap at
four. Past that, swapping is both faster *and* cheaper. The mode that extends
the serving floor is the same mode that stops scaling.

```
python3 poc/bin/experiment_scaling.py
```

## Finding 4: the occupancy decision is a device property

Whether model A can stay resident depends on whether model B is there. The
controller therefore chooses a mode once for the group, from the most
demanding declared workload, then assigns per-model batch width inside that
mode.

```yaml
apiVersion: inference.gpushare.io/v1alpha1
kind: ElasticModel
metadata:
  name: qwen-chat
spec:
  model: Qwen/Qwen2.5-0.5B-Instruct
  memoryBudget: 1400Mi
  latencySLO: 60ms
  ttftSLO: 80ms
  workload:
    concurrency: 8
    expectedOutputTokens: 16
    requestsPerSwitch: 4
```

A budget below the measured floor is `Rejected` with the number that makes it
impossible, not an `OOMKilled` loop. An assignment the worker cannot hold
lands as `Degraded` with the worker's own reason, and serving continues in
the shape that does fit.

```
python3 poc/bin/controller.py --from-dir examples/elastic --device-budget-mb 1800
```

## Finding 5: the worker is the authority, not the profile

A prediction can be wrong — different workload, different hardware, a stale
table. Assignments are applied between batches. One that does not fit is
rolled back, recorded on `/admin/config`, and published as `Degraded`:

```
overcommitted  Degraded  worker could not adopt resident:
                         OutOfMemoryError: HIP out of memory...
```

The first version of this path killed the scheduler thread on OOM. The budget
was being honoured (785 MB of 800) while every later request timed out. A
failed reshape that does not roll back is worse than a wrong prediction.

```
python3 poc/tests/validate_serve.py       # 23 checks
python3 poc/tests/validate_controller.py  # 15 checks
python3 poc/tests/validate_host.py        # 29 checks
```

## Supporting measurements (why the numbers look the way they do)

These are not the contribution, but they are why a policy that trusts tensor
sizes or a closed-form KV formula will pick the wrong mode.

**Torch under-reports GPU memory by 1.84×.** On streamed `r=1`,
`memory_allocated()` was 373 MB and the driver showed 687 MB. A 76 MB rocBLAS
workspace and an ~182 MB driver context (growing to ~266 MB as kernels load)
make a **258 MB per-process floor** before any weight is placed.

**The usual KV formula is 29% low.** `2 * layers * kv_heads * head_dim * dtype`
is 12288 B/token here. GQA `repeat_kv` adds 3584 B/token of transient.
Budgeting 15872 B/token brought the error from 6.3 MB to 0.19 MB.

**Streaming output is byte-identical to a full-resident reference** at every
residency, for prefill and decode. The reference must be pure greedy argmax:
HuggingFace applies `repetition_penalty: 1.1` from `generation_config.json`
even when `do_sample=False`.

**Inside one model, batching is what makes streaming usable.** Streamed `r=1`
batch 4 does 4× the work for 4.03× the throughput at +116 MB, because
per-step latency barely moves. That is a single-model knob (how wide a batch
the freed memory can buy), not a reason to stream when two models can swap.

**Uncapped profiles overstate requirements.** The allocator keeps slack it
does without when capped. Feasibility is taken from the smallest budget a
shape was *observed* to run under.

```
python3 poc/tests/validate_memory.py
python3 poc/tests/validate_engine.py
python3 poc/tests/validate_profile.py
python3 poc/tests/validate_batch.py
python3 poc/tests/validate_policy.py
```

## Honest limits

- **Small models on unified memory.** 0.5B and 360M on an APU. Layer transfer
  is cheaper here than over PCIe; the *shape* of the tradeoff (per-token vs
  per-switch) should hold, the constants will not. At 70B the floor matters
  less and a swap costs more.
- **Profiles are workload-specific.** The mode table is measured at the
  concurrencies and run lengths in `experiment_multimodel.py`. A different
  mix of prompt length or batching will move the crossover.
- **No preemption.** Admission is priority-ordered. An admitted model is not
  shrunk to make room for a later, higher-priority arrival.
- **Not integrated with DRA or the Inference Extension.** This is a parallel
  prototype. Those projects account and route; this one chooses an occupancy
  mode. They compose, they do not replace each other.

## Reproducing

```bash
python3 poc/bin/profile_model.py
python3 poc/bin/benchmark_batching.py
python3 poc/bin/demo_budget.py
python3 poc/bin/experiment_multimodel.py --run-lengths 1,4 --concurrency 1,8
python3 poc/bin/experiment_switch.py
python3 poc/bin/experiment_scaling.py
python3 poc/bin/experiment_policy.py
python3 poc/bin/controller.py --from-dir examples/elastic --device-budget-mb 1800

for t in memory engine profile batch policy modes host manifests serve controller; do
  python3 poc/tests/validate_$t.py
done
```

In a cluster:

```bash
kubectl apply -f k8s/crd-elasticmodel.yaml
kubectl apply -f k8s/elastic-worker.yaml
kubectl apply -f k8s/elastic-controller.yaml
kubectl apply -f examples/elastic/four-models.yaml
kubectl -n llc get elasticmodels
```
