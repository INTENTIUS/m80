#!/usr/bin/env bash
# The README quick start, executed. Everything here is copied from README.md
# rather than written for CI, so a step that stops working here is a step
# that has stopped working for whoever reads it.
#
#   ./scripts/smoke.sh [image] [port]
#
# Needs docker and the AWS CLI. Uses no AWS account and spends nothing.
set -euo pipefail

image="${1:-m80:candidate}"
port="${2:-4290}"
name="m80-smoke-$$"

docker run -d --rm --name "$name" -p "$port:4290" "$image" -build-delay 300ms >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 60); do
    curl -sf "http://localhost:$port/_m80/health" >/dev/null && break
    sleep 0.5
done

export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_ENDPOINT_URL="http://localhost:$port" AWS_REGION=us-east-2
mv() { aws lambda-microvms "$@"; }

say() { printf '\n== %s\n' "$1"; }
fail() { printf 'smoke: %s\n' "$1" >&2; exit 1; }

say "health reports every operation implemented"
pending=$(curl -s "http://localhost:$port/_m80/health" | python3 -c \
    'import json,sys; print(",".join(json.load(sys.stdin)["coverage"]["notImplementedYet"]))')
[ -z "$pending" ] || fail "not implemented: $pending"

say "an empty account lists nothing"
[ "$(mv list-microvm-images --query 'length(items)')" = "0" ] || fail "fresh account is not empty"

say "build an image"
mv create-microvm-image --name demo \
    --base-image-arn arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1 \
    --build-role-arn arn:aws:iam::000000000000:role/demo \
    --code-artifact uri=s3://demo/code.zip >/dev/null

for _ in $(seq 1 40); do
    state=$(mv get-microvm-image-version --image-identifier demo --image-version 1.0 \
        --query state --output text)
    [ "$state" = "SUCCESSFUL" ] && break
    sleep 0.5
done
[ "$state" = "SUCCESSFUL" ] || fail "version stuck in $state"

say "run a VM"
vm=$(mv run-microvm --image-identifier demo --query microvmId --output text)
for _ in $(seq 1 40); do
    state=$(mv get-microvm --microvm-identifier "$vm" --query state --output text)
    [ "$state" = "RUNNING" ] && break
    sleep 0.5
done
[ "$state" = "RUNNING" ] || fail "VM stuck in $state"

say "call its endpoint with a token"
tok=$(mv create-microvm-auth-token --microvm-identifier "$vm" \
    --expiration-in-minutes 60 --allowed-ports 'allPorts={}' \
    --query 'authToken."X-aws-proxy-auth"' --output text)

code=$(curl -s -o /dev/null -w '%{http_code}' \
    "http://localhost:$port/_m80/vm/$vm/" -H "X-aws-proxy-auth: $tok")
[ "$code" = "200" ] || fail "endpoint answered $code with a good token"

# Recorded against real AWS: a missing token is 403, not 401, and the body is
# plain text rather than a modeled error.
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$port/_m80/vm/$vm/")
[ "$code" = "403" ] || fail "endpoint answered $code with no token, want 403"

say "the marker survives a suspend and resume"
before=$(curl -s "http://localhost:$port/_m80/vm/$vm/" -H "X-aws-proxy-auth: $tok" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["stateMarker"])')
mv suspend-microvm --microvm-identifier "$vm" >/dev/null
for _ in $(seq 1 40); do
    [ "$(mv get-microvm --microvm-identifier "$vm" --query state --output text)" = "SUSPENDED" ] && break
    sleep 0.5
done
mv resume-microvm --microvm-identifier "$vm" >/dev/null
for _ in $(seq 1 40); do
    [ "$(mv get-microvm --microvm-identifier "$vm" --query state --output text)" = "RUNNING" ] && break
    sleep 0.5
done
after=$(curl -s "http://localhost:$port/_m80/vm/$vm/" -H "X-aws-proxy-auth: $tok" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["stateMarker"])')
[ "$after" -gt "$before" ] || fail "marker went backwards across a resume: $before then $after"

say "an image with a live VM cannot be deleted"
if mv delete-microvm-image --image-identifier demo >/dev/null 2>&1; then
    fail "deleted an image while a VM referenced it"
fi

say "terminate, then the image goes"
mv terminate-microvm --microvm-identifier "$vm" >/dev/null
for _ in $(seq 1 40); do
    [ "$(mv get-microvm --microvm-identifier "$vm" --query state --output text)" = "TERMINATED" ] && break
    sleep 0.5
done
mv delete-microvm-image --image-identifier demo >/dev/null

printf '\nsmoke: the quick start works end to end\n'
