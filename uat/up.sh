#!/usr/bin/env bash
# Brings the whole stack up from nothing: a k3d cluster, m80, and the
# KubeMicroVM operator pointed at it. Idempotent — re-running recreates the
# cluster.
set -euo pipefail

usage() {
    sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//;$d'
    cat <<'USAGE'

  ./uat/up.sh

Overridable by environment variable: NS, M80_IMAGE,
CHART_VERSION, REGION, MAX_ACCOUNT_MEMORY_MIB. The cluster's name and
shape are declared in uat/cluster/cluster.ts, not overridable here.

Needs docker, k3d, kubectl, helm, node and npm. Uses no AWS account.
Guide: docs/kubemicrovm.md
USAGE
}

# This script deletes and recreates a cluster, so an unrecognised argument
# stops it rather than being ignored.
if [ "$#" -gt 0 ]; then
    case "$1" in
        -h|--help) usage; exit 0 ;;
        *) echo "up.sh takes no arguments (got '$1')" >&2; usage >&2; exit 2 ;;
    esac
fi

# The name of record is the declaration's (uat/cluster/cluster.ts). A CLUSTER
# override that disagrees would create one cluster and talk to another.
CLUSTER="${CLUSTER:-m80-uat}"
if [ "${CLUSTER}" != "m80-uat" ]; then
    echo "CLUSTER=${CLUSTER} disagrees with the declared cluster (uat/cluster/cluster.ts:" >&2
    echo "m80-uat). The declaration is where the name changes." >&2
    exit 1
fi
NS="${NS:-kube-microvm}"
M80_IMAGE="${M80_IMAGE:-ghcr.io/intentius/m80:v0.4.0}"
CHART_VERSION="${CHART_VERSION:-1.0.12}"
REGION="${REGION:-us-east-1}"
# m80 defaults to the account memory ceiling recorded from a fresh AWS account:
# 4096 MiB, which at the 2048 MiB default tier is two concurrent MicroVMs. The
# UAT runs far more than two at once and every suite that does would fail with
# 402 ServiceQuotaExceededException. A real account used for UAT would have had
# its quota raised, so the harness raises it too.
MAX_ACCOUNT_MEMORY_MIB="${MAX_ACCOUNT_MEMORY_MIB:-262144}"

echo "==> cluster ${CLUSTER}"
k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
# The cluster's shape lives in uat/cluster/cluster.ts, not in flags here —
# chant emits the SimpleConfig and k3d consumes it verbatim, ownership labels
# and all. --wait/--timeout stay flags: lifecycle, not shape.
HERE="$(cd "$(dirname "$0")" && pwd)"
(cd "${HERE}/cluster" && npm install --no-audit --no-fund >/dev/null \
    && npx chant build . -o dist/k3d-uat.yaml --format yaml >/dev/null)
# The kubeconfig flags repeat what the declared config already says, on
# purpose: k3d 5.8.3 was observed ignoring `options.kubeconfig` when it
# comes via --config (twice, across two repos), and every kubectl call
# below relies on the ambient context being this cluster. Stated as flags,
# the behaviour is deterministic whatever the config loader does.
k3d cluster create --config "${HERE}/cluster/dist/k3d-uat.yaml" \
    --kubeconfig-update-default --kubeconfig-switch-context \
    --wait --timeout 300s >/dev/null

echo "==> cert-manager (operator webhooks need it; EKS clusters in the upstream flow already have it)"
helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo update >/dev/null
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace --set crds.enabled=true \
  --wait --timeout 6m >/dev/null

# A locally built image is not in any registry, so k3d cannot pull it.
# Importing it is what lets a contributor point the harness at their own
# build: M80_IMAGE=m80:candidate ./uat/up.sh
if docker image inspect "${M80_IMAGE}" >/dev/null 2>&1; then
    echo "==> importing local image ${M80_IMAGE}"
    k3d image import "${M80_IMAGE}" -c "${CLUSTER}" >/dev/null
fi

kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "==> m80 (the MicroVMs API, and sts:GetCallerIdentity for the operator's startup gate)"
kubectl apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: { name: m80, namespace: ${NS} }
spec:
  replicas: 1
  selector: { matchLabels: { app: m80 } }
  template:
    metadata: { labels: { app: m80 } }
    spec:
      containers:
        - name: m80
          image: ${M80_IMAGE}
          args: ["-addr", ":4290", "-build-delay", "500ms", "-max-account-memory-mib", "${MAX_ACCOUNT_MEMORY_MIB}", "-serve-sts"]
          ports: [{ containerPort: 4290 }]
          readinessProbe:
            httpGet: { path: /_m80/health, port: 4290 }
            initialDelaySeconds: 2
---
apiVersion: v1
kind: Service
metadata: { name: m80, namespace: ${NS} }
spec:
  selector: { app: m80 }
  ports: [{ port: 4290, targetPort: 4290 }]
---
# The Robot runner is a container on k3d's docker network, not a pod, so it
# cannot resolve *.svc.cluster.local. Anything the suite drives with the AWS
# CLI — the drift cases terminate a MicroVM out of band with it — needs m80
# reachable by a docker-network address, which is what this NodePort is for.
apiVersion: v1
kind: Service
metadata: { name: m80-node, namespace: ${NS} }
spec:
  type: NodePort
  selector: { app: m80 }
  ports: [{ port: 4290, targetPort: 4290, nodePort: 30429 }]
YAML
kubectl -n "${NS}" rollout status deploy/m80 --timeout=300s

echo "==> KubeMicroVM operator ${CHART_VERSION}"
helm install kube-microvm-operator \
  "oci://ghcr.io/codriverlabs/helm/kube-microvm-operator" --version "${CHART_VERSION}" \
  -n "${NS}" \
  --set "app.envs.AWS_MICROVM_ENDPOINT=http://m80.${NS}.svc.cluster.local:4290" \
  --set "app.envs.AWS_REGION=${REGION}" \
  --wait --timeout 6m >/dev/null

# The chart templates only the env keys it knows, so credentials and the STS
# override have to be patched in. Both are required: the operator's startup
# gate calls sts:GetCallerIdentity with no endpoint override, and without
# credentials the SDK's default chain finds none. The override points back at
# m80, which answers that one action under -serve-sts.
kubectl -n "${NS}" set env deploy/kube-microvm-operator \
  AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
  AWS_EC2_METADATA_DISABLED=true \
  "AWS_ENDPOINT_URL_STS=http://m80.${NS}.svc.cluster.local:4290" >/dev/null
kubectl -n "${NS}" rollout status deploy/kube-microvm-operator --timeout=300s

kubectl label namespace default lambda.aws.amazon.com/manage-microvms=true --overwrite >/dev/null

echo
echo "stack up. operator health:"
# grep -m1 exits on the first match, which SIGPIPEs kubectl, which under
# pipefail makes the whole pipeline fail even though the line was found. So
# capture first and match second, or a healthy stack reports itself unhealthy.
#
# And poll, briefly: the env patches above restart the operator pod, so a
# single read races the fresh pod's startup gate — the confirmation lands a
# few seconds after the rollout reports done, and a healthy stack reported
# itself broken by exactly that gap once.
line=""
for _ in $(seq 1 24); do
    health="$(kubectl -n "${NS}" logs deploy/kube-microvm-operator --tail=200 2>/dev/null || true)"
    if line="$(printf '%s\n' "${health}" | grep -m1 'AWS connectivity confirmed')"; then
        break
    fi
    sleep 5
done
if [ -n "${line}" ]; then
    echo "  ${line}"
    echo
    echo "  next: KUBEMICROVM=/path/to/KubeMicroVM just uat-run"
else
    echo "  the operator has not confirmed connectivity within 120s."
    echo "  kubectl -n ${NS} logs deploy/kube-microvm-operator"
    exit 1
fi
