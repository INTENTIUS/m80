# m80

A standalone, stateful local emulator of the AWS Lambda MicroVMs API. Like LocalStack, but for Lambda MicroVMs. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

**Status: all 29 operations implemented.** The conformance suite runs 71 checks against recorded fixtures with nothing skipped and nothing unimplemented. See the [roadmap](docs/roadmap.md).

## Quick start

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80

curl -s localhost:4290/_m80/health | jq '.coverage.implemented, .coverage.total'
```

Point any SDK at it with an endpoint override — m80 reads the region out of the sigv4 credential scope, so one instance serves every region and the credentials need not be real:

```sh
aws configure set aws_access_key_id test
aws configure set aws_secret_access_key test

aws --endpoint-url http://localhost:4290 --region us-east-2 \
    lambda-microvms list-microvm-images
```

```go
cfg, _ := config.LoadDefaultConfig(ctx,
    config.WithRegion("us-east-2"),
    config.WithBaseEndpoint("http://localhost:4290"))
```

Transitions run on an injected clock, so `-build-delay` decides how long a build takes to become runnable. Short for tests, longer for a demo where the intermediate states should be visible:

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80 -build-delay 300ms
```

Everything is in memory and nothing is written, so a restart is a clean account. m80 is stateful within a run — image names stay reserved through the async delete window, exactly as the real service does — so a suite that runs twice against one instance will fail the second time on `already exists`. That is fidelity, not a bug; restart between runs.

m80 follows the pattern of [mudflaps](https://github.com/intentius/mudflaps) (Fly Machines) and [spritzer](https://github.com/intentius/spritzer) (Fly Sprites). A single static Go binary and distroless container that holds MicroVM images, VMs, tokens, and network connectors in memory, advances them through their lifecycle on an injected clock, and answers the real wire protocol so any SDK client works against it via endpoint override.

Nothing like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.

## Who it serves

| Consumer | How |
|----------|-----|
| [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator | SDK endpoint override, next to k3d, in their UAT and CI |
| kubemicrovm-ops, a chant adoption kit for KubeMicroVM (in design) | Local end-to-end loop for install Op and lifecycle work |
| chant fly-style activities and behold demos | Accountless local apply |
| Anyone building on the MicroVM SDK | A stateful test target instead of hand-rolled mocks |

## What it is not

CloudFormation emulation of `AWS::Lambda::MicrovmImage` lives in floci, where the CFN engine is. m80 is the full-fidelity service emulator, floci carries the narrower CFN-sufficient implementation, and one conformance suite keeps both honest. See [docs/floci.md](docs/floci.md).

Replica pools, classes, and token sidecars are KubeMicroVM constructs, not service API. m80 models the service, not the operator.

## Pointing KubeMicroVM at m80

The operator supports endpoint override out of the box (#17). All three of its SDK clients honor one env var on the operator deployment:

```
AWS_MICROVM_ENDPOINT=http://m80:4290
```

The `microvm` CLI's default token path goes through the operator's sub-resource and needs nothing. Only the CLI's `--direct` debug flag builds its own SDK client without an override.

## Calling a VM's endpoint

Each VM gets an endpoint hostname, and m80 answers it from the same process. Issue a token, then present it as `X-aws-proxy-auth`:

```sh
TOK=$(curl -s -X POST "$M80/2025-09-09/microvms/$VM/auth-token" \
  -d '{"expirationInMinutes":60,"allowedPorts":[{"allPorts":{}}]}' \
  | jq -r '.authToken["X-aws-proxy-auth"]')

# by the hostname the control plane handed out
curl "$M80/" -H "Host: $(curl -s "$M80/2025-09-09/microvms/$VM" | jq -r .endpoint)" \
  -H "X-aws-proxy-auth: $TOK"

# or by path, when forging a Host header is inconvenient
curl "$M80/_m80/vm/$VM/" -H "X-aws-proxy-auth: $TOK"
```

The response body is m80's default stub, or whatever `-vm-stub-body <file>` points at. Either way an `X-M80-State-Marker` header carries a counter that survives suspend and resume, so a client can prove the VM kept its state. A suspended VM whose idle policy sets `autoResumeEnabled` wakes when its endpoint is called.

## Throttles and quotas

The account memory ceiling is recorded live and on by default: six concurrent `RunMicrovm` calls on a fresh account admit two 2048 MiB VMs and reject four with `402 ServiceQuotaExceededException`. Throttling was never observed against the real service — the memory ceiling fires first and hides it — so it is off unless asked for.

```sh
m80 -max-account-memory-mib 8192          # raise the recorded 4096 ceiling
m80 -max-microvms 3                       # cap by count instead
m80 -max-concurrent-snapshot-creates 1    # cap in-flight image builds
m80 -throttle-requests-per-interval 10 -throttle-interval-seconds 1 \
    -throttle-reason CallerRateLimitExceeded -throttle-retry-after-seconds 5
```

`-throttle-reason` is observable on the connector and tags operations, whose models carry `TooManyRequestsException.Reason`. The MicroVM family has no such member and throttles as `ThrottlingException` instead.

**The memory ceiling is the one that catches people.** 4096 MiB is the number recorded from a fresh AWS account, and at the 2048 MiB default tier it is two concurrent MicroVMs — a third gets `402 ServiceQuotaExceededException`. The recorded body names no number and no knob, so m80 logs one:

```
WARN run rejected: account memory ceiling reached allocatedMiB=4096 requestedMiB=2048
     ceilingMiB=4096 hint="raise -max-account-memory-mib, or 0 to uncap"
```

Anything running more than two VMs at once — a ReplicaSet, an operator test suite — wants `-max-account-memory-mib` raised. Pointing KubeMicroVM's UAT at m80 lost twenty of twenty-eight cases to this before the ceiling was raised, and the operator reported it as a timeout.

## Operator proof

KubeMicroVM's Robot Framework UAT — 63 cases written for a live EKS cluster and a real AWS account — runs on k3d against m80. **50 of 63 pass**, run 2026-08-01 against m80 v0.1.0 and operator 1.0.11.

| | Suite | Passed |
|---|---|---|
| ⚠️ | 00 Cluster Setup | 6/7 |
| ⚠️ | 01 Quick Start | 7/9 |
| ⚠️ | 02 RBAC | 6/8 |
| ⚠️ | 03 Networking | 2/5 |
| ⚠️ | 04 Pod Token Injection | 8/9 |
| ⚠️ | 05 ReplicaSet | 5/6 |
| ✅ | 06 MicroVMClass | 6/6 |
| ⚠️ | 07 Drift & Auto-Suspend | 4/5 |
| ⚠️ | 08 Memory Sizing | 5/6 |
| ⚠️ | 99 Final Cleanup | 1/2 |

None of the thirteen failures is m80 answering differently from real AWS — and in three of them, m80 answering *exactly* as real AWS is what exposes a bug in the operator. Four reach the VM endpoint hostname, which resolves to real AWS rather than to m80; four lose a race between the operator's resync and the UAT's 60s timeout; two want an IAM decision m80 refuses to make by design; three hit an operator finalizer that never clears because terminated MicroVMs stay listed, exactly as the real service does. The harness, every deviation from the upstream UAT, and the full breakdown are in [uat/README.md](uat/README.md).

## Development

Go 1.25, [just](https://github.com/casey/just) as the task runner. `just` lists the recipes; `just build`, `just test`, and `just fmt-check` match CI. The plan is tracked in [epic #22](https://github.com/INTENTIUS/m80/issues/22).

## Design documents

| Doc | Contents |
|-----|----------|
| [docs/scope.md](docs/scope.md) | What m80 emulates and what it refuses |
| [docs/api-surface.md](docs/api-surface.md) | The operations, their sources of truth, wire fidelity contract |
| [docs/lifecycle.md](docs/lifecycle.md) | The VM and image state machines on an injected clock |
| [docs/conformance.md](docs/conformance.md) | The language-agnostic suite shared with floci and real AWS |
| [docs/floci.md](docs/floci.md) | Division of labor with floci, sequencing |
| [docs/roadmap.md](docs/roadmap.md) | Milestones |
