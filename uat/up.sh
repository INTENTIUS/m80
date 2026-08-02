#!/usr/bin/env bash
# Brings the whole stack up from nothing: a k3d cluster, m80, floci for STS,
# and the KubeMicroVM operator pointed at both. Idempotent — re-running
# recreates the cluster.
set -euo pipefail

usage() {
    sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//;$d'
    cat <<'USAGE'

  ./uat/up.sh

Overridable by environment variable: CLUSTER, NS, M80_IMAGE, FLOCI_IMAGE,
CHART_VERSION, REGION, MAX_ACCOUNT_MEMORY_MIB.

Needs docker, k3d, kubectl and helm. Uses no AWS account.
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

CLUSTER="${CLUSTER:-m80-uat}"
NS="${NS:-kube-microvm}"
M80_IMAGE="${M80_IMAGE:-ghcr.io/intentius/m80:v0.2.0}"
FLOCI_IMAGE="${FLOCI_IMAGE:-ghcr.io/lex00/floci:microvm}"
CHART_VERSION="${CHART_VERSION:-1.0.11}"
REGION="${REGION:-us-east-1}"
# m80 defaults to the account memory ceiling recorded from a fresh AWS account:
# 4096 MiB, which at the 2048 MiB default tier is two concurrent MicroVMs. The
# UAT runs far more than two at once and every suite that does would fail with
# 402 ServiceQuotaExceededException. A real account used for UAT would have had
# its quota raised, so the harness raises it too.
MAX_ACCOUNT_MEMORY_MIB="${MAX_ACCOUNT_MEMORY_MIB:-262144}"

echo "==> cluster ${CLUSTER}"
k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
k3d cluster create "${CLUSTER}" --agents 1 --wait --timeout 300s >/dev/null

echo "==> cert-manager (operator webhooks need it; EKS clusters in the upstream flow already have it)"
helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo update >/dev/null
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace --set crds.enabled=true \
  --wait --timeout 6m >/dev/null

kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "==> m80 (the MicroVMs API) and floci (STS only)"
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
          args: ["-addr", ":4290", "-build-delay", "500ms", "-max-account-memory-mib", "${MAX_ACCOUNT_MEMORY_MIB}"]
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
apiVersion: apps/v1
kind: Deployment
metadata: { name: floci, namespace: ${NS} }
spec:
  replicas: 1
  selector: { matchLabels: { app: floci } }
  template:
    metadata: { labels: { app: floci } }
    spec:
      # A Service named floci injects FLOCI_PORT=tcp://... which Quarkus maps
      # onto the floci.port config property and then fails to parse as an int.
      enableServiceLinks: false
      containers:
        - name: floci
          image: ${FLOCI_IMAGE}
          ports: [{ containerPort: 4566 }]
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
---
apiVersion: v1
kind: Service
metadata: { name: floci, namespace: ${NS} }
spec:
  selector: { app: floci }
  ports: [{ port: 4566, targetPort: 4566 }]
YAML
kubectl -n "${NS}" rollout status deploy/m80 --timeout=300s
kubectl -n "${NS}" rollout status deploy/floci --timeout=300s

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
# credentials the SDK's default chain finds none.
kubectl -n "${NS}" set env deploy/kube-microvm-operator \
  AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
  AWS_EC2_METADATA_DISABLED=true \
  "AWS_ENDPOINT_URL_STS=http://floci.${NS}.svc.cluster.local:4566" >/dev/null
kubectl -n "${NS}" rollout status deploy/kube-microvm-operator --timeout=300s

kubectl label namespace default lambda.aws.amazon.com/manage-microvms=true --overwrite >/dev/null

echo
echo "stack up. operator health:"
kubectl -n "${NS}" logs deploy/kube-microvm-operator --tail=200 2>/dev/null \
  | grep -m1 "AWS connectivity confirmed" || echo "  (connectivity line not seen yet)"
