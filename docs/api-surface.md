# API surface

## Source of truth

The MicroVM API ships as an SDK service model. Three copies are available and must agree.

| Source | Use |
|--------|-----|
| `service-2.json` vendored in KubeMicroVM's `operator-aws-client` | Machine-readable operation list, shapes, paginators. The extraction target for M1 |
| aws-sdk-go-v2 generated client | The wire-parity reference, the role fly-go plays for mudflaps |
| AWS API reference docs | Human-readable semantics, error taxonomy |

The first design task is extracting the operation inventory from the vendored model rather than transcribing docs. The list below is the working set from public docs and KubeMicroVM's IAM policy, to be corrected against the model.

## Working operation set

| Group | Operations |
|-------|-----------|
| Images | `CreateMicrovmImage`, `GetMicrovmImage`, `ListMicrovmImages`, `DeleteMicrovmImage`, image version listing and delete, base image listing |
| VMs | `CreateMicrovm`, `GetMicrovm`, `ListMicrovms`, `UpdateMicrovm`, suspend, resume, terminate |
| Tokens | Token issue for a VM, whatever shape the model gives it |
| Connectors | `CreateNetworkConnector`, `GetNetworkConnector`, `ListNetworkConnectors`, `DeleteNetworkConnector` |

The IAM policy in KubeMicroVM grants `lambda:*Microvm*`, `lambda:*MicrovmImage*`, and `lambda:*NetworkConnector*`, which bounds the namespace. The model extraction closes the exact list.

## Protocol notes

The service rides the Lambda endpoint family, so squib listens on one port and dispatches by operation the way the real endpoint does. The SDK's endpoint override points the whole client at squib, which is how the KubeMicroVM operator, the `microvm` CLI with `--direct`, and any SDK consumer attach.

Errors matter as much as happy paths. The conformance suite records the real service's error codes for the standard set. Not found, conflict on double-terminate, validation failures for each enforced limit, throttling shape for the quota tests KubeMicroVM's QuotaGuard exercises. Emulating the throttling envelope is what lets their rate-limiter logic be tested offline.

## The VM endpoint

Each running VM gets an endpoint URL. squib answers it from the same process, routed by host header or path prefix, returning a configurable stub body and honoring `X-aws-proxy-auth` against issued tokens. Suspended VMs answer the way the real service answers, which the conformance suite must record rather than guess.

## Health and introspection

`/_squib/health` reports implemented operations against the model inventory, the mudflaps convention. A `/_squib/clock` test hook advances the injected clock so lifecycle tests are deterministic and instant.
