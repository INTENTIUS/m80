# m80

A standalone, stateful local emulator of the AWS Lambda MicroVMs API. Like LocalStack, but for Lambda MicroVMs. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

**Status: design phase. This repo is documentation only, no code yet.** The design documents below are the current deliverable; implementation starts at M3 on the [roadmap](docs/roadmap.md).

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
