# m80

A standalone, stateful local emulator of the AWS Lambda MicroVMs API. Like LocalStack, but for Lambda MicroVMs. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

m80 follows the pattern of [mudflaps](https://github.com/intentius/mudflaps) (Fly Machines) and [spritzer](https://github.com/intentius/spritzer) (Fly Sprites). A single static Go binary and distroless container that holds MicroVM images, VMs, tokens, and network connectors in memory, advances them through their lifecycle on an injected clock, and answers the real wire protocol so any SDK client works against it via endpoint override.

Nothing like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.

## Status

Design and conformance phase. The [conformance suite](conformance.md) is built and cases cover all 29 operations; the emulator itself lands with the Phase 3 issues of [epic #22](https://github.com/INTENTIUS/m80/issues/22).

## Who it serves

| Consumer | How |
|----------|-----|
| [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator | `AWS_MICROVM_ENDPOINT=http://m80:4290` on the operator deployment, next to k3d |
| Anyone building on the MicroVM SDK | A stateful test target instead of hand-rolled mocks |
| chant activities and behold demos | Accountless local apply |

## Reading order

[Scope](scope.md) — what m80 emulates and what it refuses. [API surface](api-surface.md) — the verified 24 + 5 operation inventory and its sources of truth. [Lifecycle](lifecycle.md) — the state machines. [Conformance](conformance.md) — the suite that gates every implementation slice. [floci](floci.md) — why CloudFormation emulation lives elsewhere. [Roadmap](roadmap.md) — milestones and open questions.
