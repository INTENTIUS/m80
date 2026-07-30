# Scope

## Emulated

The Lambda MicroVMs control plane and the minimal data-plane edges a client can observe.

| Area | Behavior |
|------|----------|
| MicroVM images | Create from an S3 source reference, build lifecycle with deterministic delay, base image catalog, versions, delete and prune |
| MicroVMs | Create from an image, run, suspend, resume, terminate, idle and suspend timers |
| State preservation | A suspended VM resumes with its prior state marker intact, the eight hour cap enforced on the injected clock |
| Tokens | Issue per-VM auth tokens, validate `X-aws-proxy-auth` on the VM endpoint |
| Endpoints | Each running VM exposes an HTTPS endpoint URL, m80 answers it with a configurable stub, enough for `curl` and readiness checks |
| Network connectors | CRUD for VPC egress connectors, subnet and security group references accepted as opaque strings, service limits enforced |
| Limits | The five memory tiers, name patterns and lengths, environment variable caps, connector and subnet bounds |

## Refused, on purpose

| Not emulated | Why |
|--------------|-----|
| Actually running guest code | m80 is control-plane. The endpoint stub is observable, not a VM. mudflaps drew the same line and it held |
| CloudFormation | Lives in floci where the CFN engine is, see [floci.md](floci.md) |
| Replica pools, classes, sidecar injection | KubeMicroVM operator constructs, above the service API |
| IAM evaluation | Roles and ARNs are accepted and echoed, never evaluated. A validation toggle can reject obviously malformed ARNs |
| Real S3 | The image source reference is recorded, not fetched. An optional hook can assert the object exists against a configured S3 endpoint for integration setups that run m80 next to floci |
| Billing | Suspended-costs-nothing is a pricing fact, not wire behavior |

## Fidelity anchors

Wire shapes come from the AWS SDK service model. KubeMicroVM vendors the model JSON in `operator-aws-client/src/main/resources/codegen-resources/service-2.json`, and aws-sdk-go-v2 generates from the same source. Field parity with the generated Go client is the contract, mirroring mudflaps' fly-go rule.

Behavioral truth comes from the AWS docs and, where docs are silent, from probing the real service and recording the answer in the conformance suite. Silence in the docs is never license to invent.

## Non-goals that could become goals

x86 support tracking, if AWS adds it. GPU, likewise. Multi-region behavior beyond a region field on every resource. Each waits for the real service to move first.
