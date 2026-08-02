# m80

**Check a KubeMicroVM deployment before it costs you anything.**

Running the [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator normally means an EKS cluster and an AWS account that bills you. m80 is a local emulator of the AWS Lambda MicroVMs API, so the same operator, unmodified, runs on k3d against your laptop. Apply real CRs and watch them reconcile without provisioning anything.

```sh
just uat-up     # k3d + cert-manager + m80 + the operator, about two minutes
```

Then apply a `MicroVMImage` and a `MicroVM` and watch the operator drive them.

One emulator, 8 MiB, next to the real operator on a real cluster. No AWS account, no credentials, no bill. The [guide](docs/kubemicrovm.md) has a worked example that goes from nothing to a running MicroVM.

## Does it actually catch anything?

Running KubeMicroVM's own 63-case UAT suite against m80 surfaced three issues in the operator, none of which needed an AWS account to find.

The sharpest is a genuine bug: a MicroVM's finalizer is never removed after a successful terminate, so a deleted CR hangs forever and the operator logs `Cleaning up …` every ten seconds until someone patches the object by hand. Anyone who met that in production paid for a cluster to find it. The other two are that the operator cannot start against any emulated endpoint, and that its Helm chart silently drops env keys it does not recognise.

All three are filed upstream and acknowledged by the maintainers: [#50](https://github.com/codriverlabs/KubeMicroVM/issues/50), [#51](https://github.com/codriverlabs/KubeMicroVM/issues/51), [#52](https://github.com/codriverlabs/KubeMicroVM/issues/52).

## What it does and does not check

m80 emulates the control plane, so a MicroVM here is a record with a state machine and a clock. Nothing fetches your artifact and nothing executes it.

| Checked for real | Not checked |
|---|---|
| Does the CR reconcile, and to what | Whether your code runs |
| Lifecycle: run, suspend, resume, terminate | Latency, throughput, load |
| Drift detection and auto-suspend | Anything reading real output from the VM |
| RBAC, admission webhooks, MicroVMClass | IAM actually deciding anything |
| ReplicaSets, memory tiers, quota rejection | `microvm exec` into a shell |
| Finalizers and teardown | |

The Kubernetes half is entirely real: real cluster, real operator, real pods, real tokens injected into them. The MicroVM is where reality stops.

Answers come from recording the live service rather than from reading docs and guessing. When the per-VM endpoint was finally recorded, four of nine reasonable-looking assumptions turned out to be wrong, including one where m80 rejected requests real AWS accepts. That is the failure mode an emulator has to design against, because a test suite that passes against a wrong emulator is worse than no emulator.

**Status: released, v0.2.0.** All 29 operations implemented, the conformance suite runs 100 checks against fixtures recorded from live AWS with nothing skipped, and KubeMicroVM's UAT passes 50 of 63 with every failure accounted for. See the [roadmap](docs/roadmap.md).

## Using it directly

The operator is the reason m80 exists, but it is a standalone target for anything speaking the MicroVMs API. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

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

### The whole loop, from nothing to a running VM

Every operation the AWS CLI knows works against m80. The one thing worth saying up front is that the identifier flags are not uniform — images take `--image-identifier`, VMs take `--microvm-identifier` — which is the service's naming, not m80's.

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_ENDPOINT_URL=http://localhost:4290 AWS_REGION=us-east-2
alias mv='aws lambda-microvms'

# Build an image. Nothing is fetched, so the S3 uri and the role need not exist.
mv create-microvm-image --name demo \
    --base-image-arn arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1 \
    --build-role-arn arn:aws:iam::000000000000:role/demo \
    --code-artifact uri=s3://demo/code.zip

mv get-microvm-image-version --image-identifier demo --image-version 1.0 \
    --query state          # CREATING, then SUCCESSFUL after -build-delay

# Run one, and wait for it.
VM=$(mv run-microvm --image-identifier demo --query microvmId --output text)
mv get-microvm --microvm-identifier "$VM" --query '[state,endpoint]'

# Suspend it, resume it, terminate it.
mv suspend-microvm --microvm-identifier "$VM"
mv resume-microvm  --microvm-identifier "$VM"
mv terminate-microvm --microvm-identifier "$VM"
```

Terminated VMs stay listed, and an image cannot be deleted while a live VM references it. Both are recorded behaviour, and both surprise people.

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
| kubemicrovm-ops, a chant adoption kit for KubeMicroVM | Local end-to-end loop for install and lifecycle work |
| chant fly-style activities and behold demos | Accountless local apply |
| Anyone building on the MicroVM SDK | A stateful test target instead of hand-rolled mocks |

## What it is not

Not a CloudFormation emulator. If you deploy MicroVMs through CFN or CDK rather than through the operator, that path is served by a [floci](https://github.com/floci-io/floci) module, since CFN emulation can only live where the CFN engine lives. The two cover different deployment paths and share one conformance suite; see [docs/floci.md](docs/floci.md).

Not the operator. Replica pools, classes, and token sidecars are KubeMicroVM constructs rather than service API, so m80 models what the operator calls, not what it does.

Not a way to run your code. Covered above, and it is the limit worth repeating.

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

The response body is m80's default stub, or whatever `-vm-stub-body <file>` points at — against real AWS this is the image's own app, so it is the one part of the endpoint m80 cannot copy. Either way an `X-M80-State-Marker` header carries a counter that survives suspend and resume, so a client can prove the VM kept its state.

Everything else about the endpoint is recorded rather than guessed, as of 2026-08-01:

| Situation | Answer |
|---|---|
| No token, or one that cannot be parsed | `403 Request missing authentication` |
| A token for another VM, or a hostname naming none | `403 Token authentication failed` |
| A token that does not grant the port | `200` — `allowedPorts` does not gate this endpoint |
| Suspended, `autoResumeEnabled` set | `200`, and the VM is `RUNNING` afterwards |
| Suspended without it, or terminated | `502` with an empty body |

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

## Where the 50 of 63 comes from

Measured 2026-08-01 against operator 1.0.11, excluding the performance suite.

None of the thirteen failures is m80 answering differently from real AWS. Four reach the VM endpoint hostname, which resolves to real AWS rather than to m80. Four lose a race between the operator's resync and the UAT's 60 second timeout, which is as tight against the real service. Two want an IAM decision m80 refuses to make by design. Three are the finalizer bug above, where m80 answering *exactly* as real AWS is what exposes it.

[docs/kubemicrovm.md](docs/kubemicrovm.md) has the per-suite table, every deviation from the upstream UAT, and how to tell an m80 gap from a UAT assumption.

## Proving it works

```sh
just image-check     # build the image, run the conformance suite against it
./scripts/smoke.sh   # walk this README's quick start against the container
```

`scripts/smoke.sh` is the quick start above, executed — the same commands, in the same order, asserted. It needs docker and the AWS CLI, uses no AWS account and spends nothing. CI runs it on every pull request against a freshly built image, so a walkthrough that stops working stops the build.

The conformance suite runs in CI too, against a live binary. It compares every response to a fixture recorded from real AWS, so "m80 answers like the service" is a gate rather than a claim.

## Development

Go 1.25, [just](https://github.com/casey/just) as the task runner. `just` lists the recipes; `just build`, `just test` and `just fmt-check` match CI. `just conformance` needs a running target. The plan is tracked in [epic #22](https://github.com/INTENTIUS/m80/issues/22).

## Design documents

| Doc | Contents |
|-----|----------|
| [docs/scope.md](docs/scope.md) | What m80 emulates and what it refuses |
| [docs/api-surface.md](docs/api-surface.md) | The operations, their sources of truth, wire fidelity contract |
| [docs/lifecycle.md](docs/lifecycle.md) | The VM and image state machines on an injected clock |
| [docs/conformance.md](docs/conformance.md) | The language-agnostic suite shared with floci and real AWS |
| [docs/kubemicrovm.md](docs/kubemicrovm.md) | Standing the operator up against m80 on k3d, and the pass matrix |
| [docs/floci.md](docs/floci.md) | Division of labor with floci |
| [docs/roadmap.md](docs/roadmap.md) | Milestones and what is still open |
