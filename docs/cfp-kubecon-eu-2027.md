# KubeCon + CloudNativeCon Europe 2027 — CFP pack

Paste these fields into Sessionize. Do not invent extra claims.

| | |
|---|---|
| Event | KubeCon + CloudNativeCon Europe, Barcelona, 15–18 March 2027 |
| Main CFP | https://sessionize.com/kubecon-cloudnativecon-europe-2027/ |
| Closes | **Sunday, 11 October 2026, 11:59 PM CEST (UTC+2)** |
| Notify | Monday, 7 December 2026 |
| Questions | cfp@cncf.io |
| Reviewers score | Content, Originality, Relevance, Speaker(s), not a product pitch |

Submit **two** proposals on the main CFP (same speaker, allowed, max three):

1. **Session Presentation** (30 min) — primary
2. **Poster Session** — research / measurements; staffed at KubeCrawl

Optional third (separate form, closes **18 October**):
Cloud Native AI + Inference Day — https://sessionize.com/cncf-hosted-co-located-events-europe-2027/

Before submit: take the [Inclusive Speaker Orientation](https://training.linuxfoundation.org/training/inclusive-speaker-orientation/).
Agree to the [Code of Conduct](https://www.linuxfoundation.org/about/code-of-conduct/).
One complimentary All-Access pass per accepted speaker.

This is **not** a case study of a production fleet. It is an open-source PoC with
measured numbers on one GPU. Say that. Do not name a vendor product as the story.

---

## 1. Session Presentation (submit this first)

### Session title

Resident, Swap, or Elastic: A Kubernetes Policy for How an LLM Occupies the GPU

### Session format

Session Presentation (30 minutes, 1–2 speakers)

### Track

AI Inference and Infrastructure

If the form asks for a second choice: Emerging + Advanced.

### Audience / experience level

Intermediate. Attendees should know Kubernetes custom resources and have seen
a GPU serving stack (vLLM, Triton, or similar). No ROCm or compiler background
required.

### Is this a case study?

No.

### Presented at a CNCF / Linux Foundation event in the past year?

No.

### Session description

*(Third person. This text becomes the public schedule.)*

Kubernetes can already account for GPU memory. Dynamic Resource Allocation
counts it; the Gateway API Inference Extension routes on queue depth and
KV-cache pressure. Neither tells a serving engine **how** to occupy the
memory it was granted. An engine given half a GPU does not serve at half
scale. It fails to start. In practice, “share the GPU” still means one
workload gets the device.

This talk introduces a small Kubernetes operator and an `ElasticModel`
custom resource that close that gap. The user declares a model, a memory
budget, and an optional latency SLO. They never set a serving mode. The
first time the operator sees that model on a given GPU, it profiles it.
It then assigns one of three occupancy modes: **resident** (every admitted
model fully on the device), **swap** (one full model on the GPU, the
others waiting in host RAM), or **elastic** (a slice of layers resident,
the rest streamed per token). The serving worker is the authority: a
shape that does not fit is refused and the object becomes `Degraded`,
instead of a silent out-of-memory death.

The modes are not interchangeable. On a Radeon 8060S, two small instruction
models, and an enforced budget, the policy agreed with the hardware in
six of six scripted scenarios. At 1200 MB only elastic served (~150
tokens/s batched). At 1800 MB swap was about four times faster than
elastic. At 2600 MB resident won. A first-token SLO of 55 ms was the one
case where the policy correctly chose the slower mode, because a swap
missed the SLO. Elastic is not claimed to be faster. It is the mode that
still serves when the others cannot.

Attendees will see the custom resource, the first-seen profile Job, the
mode decision, and a live completion. They will also see the honest
limits: small models, one AMD GPU, no DRA integration. The point is the
missing policy layer, not a new serving engine.

### Benefits to the ecosystem

*(Why reviewers should care.)*

Inference on Kubernetes is adding ways to **count** accelerators and
**route** requests. It still lacks a way to **occupy** a grant that is
smaller than a model’s natural footprint. That leaves operators with a
binary: reserve a whole GPU, or watch the pod `OOMKilled`.

This session gives the community a concrete, measurable proposal for that
layer: a CRD, an operator that profiles once and assigns a mode, and a
worker that can refuse. It is designed to sit next to Dynamic Resource
Allocation and the Gateway API Inference Extension, not replace them.
DRA still accounts. The Inference Extension still routes. Occupancy
decides how the weights sit in the bytes those projects already grant.

The measurements are reproducible and negative results are included.
Elastic loses to swap whenever swap fits. Torch allocator numbers
under-report driver memory by almost 2× on this hardware. Those facts
matter to anyone writing a GPU scheduler or a serving operator, whether
or not they adopt this prototype. The code and the commands that produce
every table are open.

### What attendees will take away

- Why accounting and routing are not enough when a model does not fit.
- The three occupancy modes, and when each one actually wins.
- How a first-seen profile Job plus an `ElasticModel` budget become a mode,
  without the user writing `mode: elastic`.
- Why the serving worker must be allowed to refuse, and what `Degraded`
  is for.
- What does **not** transfer: large models, NVIDIA (untested), DRA/Gateway
  integration.

### CNCF projects this talk focuses on

- Kubernetes (custom resources, operators, Jobs, ConfigMaps)
- Related landscape, not the implementation: Dynamic Resource Allocation,
  Gateway API Inference Extension

### Other open source

- This PoC: GPUShare-LLM (engine, worker, CRD) and gpushare-operator
- PyTorch, Hugging Face Transformers
- k3s used in the development cluster

### Additional resources

Paste after the repos are pushed. Until then, attach the local writeup.

- Measured results (every table has a command): `docs/kubecon-submission.md`
- Design: `docs/elastic-occupancy.md`
- How to run: `guide.md`
- Engine repo: https://github.com/1devspace/k8s-llm-layer-compute
- Operator repo: https://github.com/1devspace/gpushare-operator

### Notes to the program committee

*(If Sessionize has a private notes field.)*

This is an open research PoC, not a product announcement and not a
production case study. All numbers are from one AMD Radeon 8060S (ROCm,
fp16) with Qwen2.5-0.5B and SmolLM2-360M / 135M. We will say that on
stage. We are not asking the audience to replace vLLM. vLLM remains the
right resident backend; this worker exists so elastic and in-process swap
can be measured at all.

The talk is a 30-minute technical session with a short live path:
ElasticModel → profile → assigned mode → one completion. No sales slides.

---

## 2. Poster Session (second submission)

Same speaker limit: one or two presenters. Staff the poster during
KubeCrawl; a co-presenter helps.

### Session title

When Sharing a GPU Still Means One Workload Gets It: Measured Occupancy Modes for LLM Inference

### Session format

Poster Session

### Track

AI Inference and Infrastructure

(Poster guidance also fits Emerging + Advanced. Prefer the same track as
the session so chairs can pair them.)

### Poster description

*(Third person.)*

Dynamic Resource Allocation can divide a GPU on paper. A serving pod given
less memory than a model’s footprint still fails to start. This poster
reports measurements of three occupancy strategies on one device —
resident, swap from host RAM, and per-token layer streaming (elastic) —
and a Kubernetes operator that assigns the strategy from a budget and an
optional SLO.

On a Radeon 8060S with two small instruction models, only elastic served
at a 1200 MB cap. Once 1800 MB was available, swap reached about 650
tokens/s against elastic’s 165. Resident needed about 2600 MB to hold
both models and then matched swap. A 55 ms first-token SLO flipped the
choice back to elastic, because a model switch cost ~59 ms. The operator
agreed with the hardware in six of six scripted scenarios and never
selected elastic when swap was faster and allowed.

The poster also shows why a closed-form memory formula is the wrong
input to that decision: on this stack the torch allocator under-reports
driver-visible memory by 1.84×, and the usual KV-cache byte formula is
29% low. The accompanying open-source operator profiles a model the
first time it appears, writes a ConfigMap, and records `Ready`,
`Rejected`, or `Degraded` on an `ElasticModel` object.

Limits are stated on the poster: small models, unified-memory AMD
hardware, no DRA hook. The invitation is to treat occupancy as a first-class
Kubernetes decision, next to accounting and routing.

### Benefits to the ecosystem

Same substance as the session, shorter: give platform and inference
engineers a measured picture of the occupancy tradeoff, and a CRD they
can read, rather than another claim that layer streaming is universally
faster.

### Additional resources

Same links as the session. If you have a paper later, add it here; the
CFP asks for a published-paper URL on posters when one exists.

---

## 3. Optional: Cloud Native AI + Inference Day

Use the **session** title and description almost unchanged. Change the
opening sentence to mention Inference Day’s focus on serving and
accelerator utilization.

Colocated CFP: https://sessionize.com/cncf-hosted-co-located-events-europe-2027/  
Closes: Sunday, 18 October 2026, 11:59 PM CET.

You may be listed on at most **three** proposals across the main CFP.
A colocated talk is a separate form; still stay inside the “one session +
one panel” selection cap if both are accepted.

---

## 4. Speaker fields (fill these yourself)

Sessionize needs these on the speaker profile, not in the abstract.

| Field | What to put |
|---|---|
| Full name | |
| Pronouns | |
| Job title | |
| Company / affiliation | |
| Company is an end user? | Only if you use cloud native internally and do not sell it. See the [end-user list](https://www.cncf.io/enduser/). If unsure: cfp@cncf.io |
| City / country | |
| Headshot | High-res, recent |
| Bio (third person, ~80–120 words) | Role, what you built, why GPU occupancy, no product pitch |
| LinkedIn / GitHub / social | |
| Previous talk recording | Required if you have one. Else a 2–3 minute unlisted YouTube of you explaining the three modes from a whiteboard |
| Dietary / a11y | Private, for ops |

### Bio template

> [Name] works on [team / org], where they [one sentence]. They built
> the occupancy operator discussed in this session: an `ElasticModel`
> custom resource, a first-seen GPU profile Job, and a worker that
> assigns resident, swap, or elastic from a memory budget instead of
> from a user-written mode. Their measurements are on AMD ROCm. They
> care about making Kubernetes inference fail with a reason (`Rejected`,
> `Degraded`) instead of `OOMKilled`.

---

## 5. Thirty-minute outline (for you, not the form)

Use this if Sessionize asks for an outline, or after acceptance.

| Min | What |
|---|---|
| 0–3 | The gap: DRA counts, Gateway routes, the pod still OOMs |
| 3–8 | Three modes, one picture each. Elastic is not faster |
| 8–14 | Table: 1200 / 1800 / 2600 MB and the TTFT SLO flip |
| 14–18 | Why formulas lie (allocator 1.84×, KV 29% low) |
| 18–24 | Live: apply `ElasticModel` → Profiling → Ready → curl |
| 24–27 | Worker as authority, `Degraded`, what we did not build |
| 27–30 | Questions |

Do not spend the slot on vLLM vs this worker. One sentence: vLLM is the
resident engine; this worker exists so the other two modes can run.

---

## 6. Claims you may use / must not use

**Use (measured):**

- Policy matched hardware in 6/6 scripted scenarios
- 1200 MB: elastic only (~151 tok/s batched; swap and resident OOM)
- 1800 MB: swap ~653 vs elastic ~165 tok/s
- 2600 MB: resident ~675 tok/s
- 1800 MB + TTFT ≤ 55 ms: elastic (swap first token ~58.8 ms)
- Swap from host RAM, not disk; disk reload is never competitive once warm
- Torch `memory_allocated` 373 MB vs driver 687 MB on streamed r=1
- Process floor ~258 MB before weights
- First-seen profile on this GPU; user never sets `mode`

**Do not use:**

- “Elastic is faster”
- “Production-ready” / “works on any cluster”
- “Replaces vLLM / DRA / Gateway API Inference Extension”
- “Tested on NVIDIA CUDA”
- Large-model or multi-node claims
- Company product names as the subject of the talk

---

## 7. Submit checklist

- [ ] Inclusive Speaker Orientation done
- [ ] Speaker profile complete (bio, headshot, time zone)
- [ ] Previous-talk video, or a short unlisted clip
- [ ] Session Presentation pasted (title, description, benefits, track)
- [ ] Poster Session pasted (different title, research framing)
- [ ] Repos pushed so Additional Resources URLs resolve
- [ ] Second speaker added only if they will actually present
- [ ] No vendor slide implied in the abstract
- [ ] Submit before **11 October 2026, 11:59 PM CEST**

Supporting writeups already in this repo:

- [kubecon-submission.md](kubecon-submission.md) — tables and commands
- [elastic-occupancy.md](elastic-occupancy.md) — design
- [guide.md](../guide.md) — how to run
