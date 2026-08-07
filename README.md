# m80

**Check a KubeMicroVM deployment before it costs you anything.**

Running the [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator normally means an EKS cluster and an AWS account that bills you. m80 is a local emulator of the AWS Lambda MicroVMs API, so the same operator, unmodified, runs on k3d against your laptop.

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80
```

One container, 8 MiB, no companion process. Point any AWS SDK at it with an endpoint override.

It emulates the **control plane**: a MicroVM here is a record with a state machine and a clock. Configuration, reconciliation, lifecycle, RBAC, quotas and teardown are real; nothing fetches your artifact and nothing runs it.

## Give this to your agent

```
There is a local emulator of the AWS Lambda MicroVMs API at http://localhost:4290
(start it with: docker run --rm -p 4290:4290 ghcr.io/intentius/m80).

Point any AWS SDK or the AWS CLI at it with an endpoint override. Credentials
need not be real — it reads the region out of the sigv4 credential scope, so one
instance serves every region.

  aws --endpoint-url http://localhost:4290 --region us-east-2 lambda-microvms list-microvm-images

It emulates the control plane only: nothing fetches your artifact and nothing
runs it. Before assuming a behaviour, read these two pages —
  https://intentius.github.io/m80/scope/       what it emulates and what it refuses
  https://intentius.github.io/m80/unrecorded/  the seven places it answers something
                                               it never observed, and the rule for it

Two things surprise people: terminated VMs stay listed, and an image cannot be
deleted while a live VM references it. Both are recorded behaviour, not bugs.
```

## Where to go

| | |
|---|---|
| [Using it](docs/using-it.md) | The whole loop, from nothing to a running VM |
| [Standing up KubeMicroVM](docs/kubemicrovm.md) | The operator on k3d against m80, and the pass matrix |
| [Scope](docs/scope.md) | What it emulates and what it refuses |
| [When m80 has not seen it](docs/unrecorded.md) | Where it is guessing, and the rule |
| [API surface](docs/api-surface.md) | The 29 operations and their sources of truth |
| [Lifecycle](docs/lifecycle.md) | The state machines, and the failure-injection levers |
| [Conformance](docs/conformance.md) | The suite that gates it |
| [floci](docs/floci.md) | Why CloudFormation emulation lives elsewhere |
| [Issues](https://github.com/INTENTIUS/m80/issues) | What is planned or open — the only place that lives |

The full documentation site is at [intentius.github.io/m80](https://intentius.github.io/m80/).

## Status

Released. All 29 operations answer, `/_m80/health` reports 29/29 with nothing pending, and the conformance suite runs 101 checks against fixtures recorded from live AWS.

Running KubeMicroVM's own 63-case UAT suite against m80 surfaced three issues in the operator — the sharpest a finalizer that never clears after a successful terminate, so a deleted CR hangs forever. All three were filed upstream; the finalizer fix shipped in their v1.0.12, verified here the day after. None needed an AWS account to find. 52 of the 63 pass, with [every failure accounted for](docs/kubemicrovm.md), and the suite runs against every commit in CI holding exactly that matrix.

Nothing else like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.

## Development

Go 1.25, [just](https://github.com/casey/just) as the task runner. `just` lists the recipes; `just build`, `just test` and `just fmt-check` match CI.

```sh
just image-check     # build the image, run the conformance suite against it
./scripts/smoke.sh   # walk the quick start against the container
```

Both run in CI on every pull request, so a walkthrough that stops working stops the build. The plan is tracked in [epic #22](https://github.com/INTENTIUS/m80/issues/22).
