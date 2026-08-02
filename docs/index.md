# m80

A standalone, stateful local emulator of the AWS Lambda MicroVMs API. Like LocalStack, but for Lambda MicroVMs. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

A single static Go binary and distroless container that holds MicroVM images, VMs, tokens, and network connectors in memory, advances them through their lifecycle on an injected clock, and answers the real wire protocol so any SDK client works against it via endpoint override. It follows the pattern of [mudflaps](https://github.com/intentius/mudflaps) (Fly Machines) and [spritzer](https://github.com/intentius/spritzer) (Fly Sprites).

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80
```

The [README](https://github.com/INTENTIUS/m80#readme) has the quick start, from `docker run` to a running MicroVM.

## Status

Released. All 29 operations answer, `/_m80/health` reports 29/29 with nothing pending, and the conformance suite runs 100 checks against fixtures recorded from live AWS.

The external check is [KubeMicroVM's own UAT suite](kubemicrovm.md), which passes 50 of its 63 cases against m80 with every failure accounted for.

Nothing else like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.

## Who it serves

| Consumer | How |
|----------|-----|
| [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator | `AWS_MICROVM_ENDPOINT` on the operator deployment, next to k3d. [Guide](kubemicrovm.md) |
| Anyone building on the MicroVM SDK | A stateful test target instead of hand-rolled mocks |
| kubemicrovm-ops, a chant adoption kit for KubeMicroVM | Local end-to-end loop for install and lifecycle work |
| chant activities and behold demos | Accountless local apply |

## Reading order

[Scope](scope.md) is what m80 emulates and what it refuses. [API surface](api-surface.md) is the 29-operation inventory, its sources of truth, and everything recording the live service corrected. [Lifecycle](lifecycle.md) is the state machines. [Conformance](conformance.md) is the suite that gates it. [Standing up KubeMicroVM](kubemicrovm.md) is the operator harness. [floci](floci.md) is why CloudFormation emulation lives elsewhere. [Roadmap](roadmap.md) is where this went and what is left.
