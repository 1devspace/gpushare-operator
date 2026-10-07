#!/usr/bin/env bash
# Prove the operator against a real Kubernetes API.
# Usage: KUBECONFIG=/tmp/k3s.yaml ./e2e/cluster_proof.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POC="${POC:-$ROOT/../GPUShare-LLM}"
NS=llc
MODEL="Qwen/Qwen2.5-0.5B-Instruct"
export PATH="${HOME}/.local/go/bin:${HOME}/bin:${PATH}"
export KUBECONFIG="${KUBECONFIG:-/tmp/k3s.yaml}"

echo "== 1. install CRD, RBAC, profiler SA"
kubectl apply -f "$ROOT/config/crd/elasticmodel.yaml"
kubectl apply -f "$ROOT/config/rbac/controller.yaml"
kubectl apply -f "$ROOT/config/operands/profiler.yaml"
kubectl wait --for=condition=Established crd/elasticmodels.inference.gpushare.io --timeout=30s

echo "== 2. user applies ElasticModel (no mode)"
kubectl -n "$NS" delete elasticmodel qwen-proof --ignore-not-found
kubectl -n "$NS" delete job profile-qwen-qwen2-5-0-5b-instruct --ignore-not-found
kubectl -n "$NS" delete cm elastic-profile-qwen-qwen2-5-0-5b-instruct --ignore-not-found
cat <<'YAML' | kubectl apply -f -
apiVersion: inference.gpushare.io/v1alpha1
kind: ElasticModel
metadata:
  name: qwen-proof
  namespace: llc
spec:
  model: Qwen/Qwen2.5-0.5B-Instruct
  memoryBudget: 1400Mi
  ttftSLO: 80ms
  maxSessions: 1
  maxContextTokens: 512
  priority: 10
YAML
kubectl -n "$NS" get em qwen-proof -o jsonpath='spec.model={.spec.model} budget={.spec.memoryBudget} mode={.status.assigned.mode}{"\n"}'

echo "== 3. operator first reconcile (should create Job, phase Profiling)"
go build -o "$ROOT/bin/controller" "$ROOT/cmd/controller"
"$ROOT/bin/controller" \
  --kubeconfig "$KUBECONFIG" \
  --namespace "$NS" \
  --device-budget-mb 1200 \
  --profiler-stub \
  --workspace-pvc="" \
  --hf-cache-pvc=""
echo "--- cluster after first reconcile ---"
kubectl -n "$NS" get em qwen-proof -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,MODE:.status.assigned.mode,REASON:.status.reason
kubectl -n "$NS" get job,cm -l app.kubernetes.io/component=profiler -o wide || true
kubectl -n "$NS" get job profile-qwen-qwen2-5-0-5b-instruct -o yaml | head -25

echo "== 4. GPU profile (same script the Job runs)"
PROF=/tmp/cluster-proof-profile.json
PYTHONPATH="$POC/poc" python3 "$POC/poc/bin/profile_job.py" \
  --model "$MODEL" --out "$PROF"
python3 - <<'PY'
import json
d=json.load(open("/tmp/cluster-proof-profile.json"))
assert d["elastic"]["driver_mb"] < d["resident"]["driver_mb"]
print(f"profile ok source={d.get('source')} elastic={d['elastic']['driver_mb']:.0f} resident={d['resident']['driver_mb']:.0f}")
PY
kubectl -n "$NS" create configmap elastic-profile-qwen-qwen2-5-0-5b-instruct \
  --from-file=profile.json="$PROF" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NS" annotate cm elastic-profile-qwen-qwen2-5-0-5b-instruct \
  inference.gpushare.io/model="$MODEL" --overwrite
kubectl -n "$NS" label cm elastic-profile-qwen-qwen2-5-0-5b-instruct \
  app.kubernetes.io/component=profiler \
  app.kubernetes.io/managed-by=elastic-operator --overwrite

echo "== 5. operator second reconcile (should assign mode)"
"$ROOT/bin/controller" \
  --kubeconfig "$KUBECONFIG" \
  --namespace "$NS" \
  --device-budget-mb 1200 \
  --profiler-stub \
  --workspace-pvc="" \
  --hf-cache-pvc=""
echo "--- cluster after second reconcile ---"
kubectl -n "$NS" get em qwen-proof -o yaml | sed -n '/^status:/,/^$/p'
PHASE=$(kubectl -n "$NS" get em qwen-proof -o jsonpath='{.status.phase}')
MODE=$(kubectl -n "$NS" get em qwen-proof -o jsonpath='{.status.assigned.mode}')
LAYERS=$(kubectl -n "$NS" get em qwen-proof -o jsonpath='{.status.assigned.residentLayers}')
test "$PHASE" = Ready
test "$MODE" = elastic
echo "PASS cluster: phase=$PHASE mode=$MODE layers=$LAYERS"
