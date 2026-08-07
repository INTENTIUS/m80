#!/usr/bin/env bash
# Runs KubeMicroVM's Robot Framework UAT against the stack up.sh built.
#
# The suite runs inside a container, not on the host: the microvm CLI ships
# linux/amd64 and linux/arm64 only, so a macOS host cannot run it at all. The
# container joins k3d's docker network and talks to the cluster by its
# internal address.
set -euo pipefail

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    cat <<'USAGE'
  KUBEMICROVM=/path/to/KubeMicroVM ./uat/run.sh [robot args...]

Runs KubeMicroVM's UAT suite against the stack up.sh built. Extra arguments
are passed through to Robot Framework, so a single suite can be selected with
--suite, or a case with --test.

Overridable by environment variable: CLUSTER, NS, REGION, ACCOUNT_ID,
CHART_VERSION, RESULTS.

Guide: docs/kubemicrovm.md
USAGE
    exit 0
fi

CLUSTER="${CLUSTER:-m80-uat}"
NS="${NS:-kube-microvm}"
REGION="${REGION:-us-east-1}"
ACCOUNT_ID="${ACCOUNT_ID:-000000000000}"
CHART_VERSION="${CHART_VERSION:-1.0.12}"
KUBEMICROVM="${KUBEMICROVM:?set KUBEMICROVM to a KubeMicroVM checkout}"
RESULTS="${RESULTS:-$(pwd)/uat-results}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker build -q -t m80-uat-runner "${HERE}" >/dev/null

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
k3d kubeconfig get "${CLUSTER}" > "${work}/kubeconfig.yaml"
# The host kubeconfig points at 0.0.0.0:<mapped>; from inside the k3d network
# the API server is the server container on 6443.
sed -i.bak -E "s#server: https://0\.0\.0\.0:[0-9]+#server: https://k3d-${CLUSTER}-server-0:6443#" \
  "${work}/kubeconfig.yaml"

mkdir -p "${RESULTS}"
docker run --rm --network "k3d-${CLUSTER}" \
  -v "${work}/kubeconfig.yaml:/kc:ro" -e KUBECONFIG=/kc \
  -e "AWS_MICROVM_ENDPOINT=http://m80.${NS}.svc.cluster.local:4290" \
  -e "AWS_ENDPOINT_URL=http://k3d-${CLUSTER}-server-0:30429" \
  -e "AWS_REGION=${REGION}" -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  -v "${KUBEMICROVM}/uat:/uat" -v "${RESULTS}:/results" \
  m80-uat-runner --outputdir /results --exclude performance \
  --variable "REGION:${REGION}" --variable "ACCOUNT_ID:${ACCOUNT_ID}" \
  --variable CODEBASE_PATH:/uat --variable "CHART_VERSION:${CHART_VERSION}" \
  "$@" tests/
