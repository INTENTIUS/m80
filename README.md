# squib

A standalone, stateful local emulator of the AWS Lambda MicroVMs API. Like LocalStack, but for Lambda MicroVMs. The name is film slang. A squib is the rig that simulates an explosion, which is what an emulator of a Firecracker-backed service does.

**Status: design phase. This repo is documentation only. No code yet, and it stays local until the design settles.**

squib follows the pattern of [mudflaps](https://github.com/intentius/mudflaps) (Fly Machines) and [spritzer](https://github.com/intentius/spritzer) (Fly Sprites). A single static Go binary and distroless container that holds MicroVM images, VMs, tokens, and network connectors in memory, advances them through their lifecycle on an injected clock, and answers the real wire protocol so any SDK client works against it via endpoint override.

Nothing like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.

## Who it serves

| Consumer | How |
|----------|-----|
| [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator | SDK endpoint override, next to k3d, in their UAT and CI |
| [kubemicrovm-ops](../kubemicrovm-ops/) kit | Local end-to-end loop for install Op and lifecycle work |
| chant fly-style activities and behold demos | Accountless local apply |
| Anyone building on the MicroVM SDK | A stateful test target instead of hand-rolled mocks |

## What it is not

CloudFormation emulation of `AWS::Lambda::MicrovmImage` lives in floci, where the CFN engine is. squib is the full-fidelity service emulator, floci carries the narrower CFN-sufficient implementation, and one conformance suite keeps both honest. See [docs/floci.md](docs/floci.md).

Replica pools, classes, and token sidecars are KubeMicroVM constructs, not service API. squib models the service, not the operator.

## Design documents

| Doc | Contents |
|-----|----------|
| [docs/scope.md](docs/scope.md) | What squib emulates and what it refuses |
| [docs/api-surface.md](docs/api-surface.md) | The operations, their sources of truth, wire fidelity contract |
| [docs/lifecycle.md](docs/lifecycle.md) | The VM and image state machines on an injected clock |
| [docs/conformance.md](docs/conformance.md) | The language-agnostic suite shared with floci and real AWS |
| [docs/floci.md](docs/floci.md) | Division of labor with floci, sequencing |
| [docs/roadmap.md](docs/roadmap.md) | Milestones |
