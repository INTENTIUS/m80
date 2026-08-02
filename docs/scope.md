# Scope

## Emulated

The Lambda MicroVMs control plane and the minimal data-plane edges a client can observe.

| Area | Behavior |
|------|----------|
| MicroVM images | Create from an S3 source reference, build lifecycle with deterministic delay, base image catalog, versions, asynchronous delete |
| MicroVMs | Create from an image, run, suspend, resume, terminate, idle and suspend timers |
| State preservation | A suspended VM resumes with its prior state marker intact, the eight hour cap enforced on the injected clock |
| Tokens | Issue per-VM auth tokens, validate `X-aws-proxy-auth` on the VM endpoint |
| Endpoints | Each running VM exposes an HTTPS endpoint URL, m80 answers it with a configurable stub, enough for `curl` and readiness checks |
| Network connectors | CRUD for VPC egress connectors, subnet and security group references accepted as opaque strings, service limits enforced |
| `sts:GetCallerIdentity` | Off unless `-serve-sts`. A shim for consumers whose startup gate calls it, not an STS emulation: every other action answers 501. See [standing up KubeMicroVM](kubemicrovm.md) |
| Limits | The five memory tiers, name patterns and lengths, connector subnet and security-group bounds, token expiry and port grants, the recorded account memory ceiling |

## Refused, on purpose

| Not emulated | Why |
|--------------|-----|
| Actually running guest code | m80 is control-plane. The endpoint stub is observable, not a VM. mudflaps drew the same line and it held |
| CloudFormation | Lives in floci where the CFN engine is, see [floci.md](floci.md) |
| Replica pools, classes, sidecar injection | KubeMicroVM operator constructs, above the service API |
| IAM evaluation | Roles and ARNs are accepted and echoed, never evaluated |
| STS beyond one action | `-serve-sts` answers `GetCallerIdentity` and refuses the rest. Anything needing `AssumeRole` wants a real AWS emulator |
| Real S3 | The image source reference is recorded, not fetched |
| Billing | Suspended-costs-nothing is a pricing fact, not wire behavior |

## Why the refusals are safe for the operator

KubeMicroVM depends on four AWS SDK clients and no others: `lambdamicrovms`, `lambdacore`, `sts` and `servicequotas`. There is no S3, IAM, EC2 or CloudFormation client anywhere in it. Role ARNs and S3 URIs are strings it hands to the MicroVMs API, so accepting them as opaque costs the operator nothing.

The work those strings cause — the service assuming the role, fetching the object, building the image, EC2 creating ENIs — happens inside AWS and is unreachable by any emulator. See [division of labor with floci](floci.md).

## Fidelity anchors

Wire shapes come from the AWS SDK service model. KubeMicroVM vendors the model JSON in `operator-aws-client/src/main/resources/codegen-resources/service-2.json`, and aws-sdk-go-v2 generates from the same source. Field parity with the generated Go client is the contract, mirroring mudflaps' fly-go rule.

Behavioral truth comes from the AWS docs and, where docs are silent, from probing the real service and recording the answer in the conformance suite. Silence in the docs is never license to invent.

## Non-goals that could become goals

x86 support tracking, if AWS adds it. GPU, likewise. Multi-region behavior beyond a region field on every resource. Each waits for the real service to move first.
