# API surface

## Source of truth

The MicroVM API ships as an SDK service model. Three copies are available and must agree.

| Source | Use |
|--------|-----|
| `service-2.json` vendored in KubeMicroVM's `operator-aws-client` | Machine-readable operation list, shapes, paginators. The extraction target for M1 |
| aws-sdk-go-v2 generated client | The wire-parity reference, the role fly-go plays for mudflaps |
| AWS API reference docs | Human-readable semantics, error taxonomy |

Cross-checked 2026-07-29 (#2). aws-sdk-go-v2 ships both services (`service/lambdamicrovms`, `service/lambdacore`; Smithy models `lambda-microvms.json` and `lambda-core.json` under `codegen/sdk-codegen/aws-models/`). Operation lists and every enum match the vendored copies exactly. The SDK's Smithy models are canonical for the wire-parity contract from here, since AWS publishes them continuously. The sigv4 signing name is `lambda` for both services (sdkIds `Lambda Microvms`, `Lambda Core`), which is what the conformance harness signs with.

## Verified operation inventory

Extracted 2026-07-29 from the vendored models. Two services, not one. `Lambda Microvms` (`apiVersion 2025-09-09`, rest-json) carries images, VMs, and tokens. `Lambda Core` (`apiVersion 2026-04-30`, rest-json, URI prefix `/2026-04-04/`) carries network connectors. Tagging rides the classic Lambda tags API at `/2017-03-31/tags/`. One emulator endpoint must route all three URI families.

### Lambda Microvms — 24 operations

| Group | Operation | Route |
|-------|-----------|-------|
| VMs | `RunMicrovm` | `POST /2025-09-09/microvms` |
| | `GetMicrovm` | `GET /2025-09-09/microvms/{microvmIdentifier}` |
| | `ListMicrovms` | `GET /2025-09-09/microvms` |
| | `SuspendMicrovm` | `POST .../{microvmIdentifier}/suspend` |
| | `ResumeMicrovm` | `POST .../{microvmIdentifier}/resume` |
| | `TerminateMicrovm` | `DELETE /2025-09-09/microvms/{microvmIdentifier}` |
| Tokens | `CreateMicrovmAuthToken` | `POST .../{microvmIdentifier}/auth-token` |
| | `CreateMicrovmShellAuthToken` | `POST .../{microvmIdentifier}/shell-auth-token` |
| Images | `CreateMicrovmImage`, `GetMicrovmImage`, `ListMicrovmImages`, `DeleteMicrovmImage`, `UpdateMicrovmImage` | `/2025-09-09/microvm-images...` |
| Versions | `GetMicrovmImageVersion`, `ListMicrovmImageVersions`, `DeleteMicrovmImageVersion`, `UpdateMicrovmImageVersion` | `.../versions/{imageVersion}` |
| Builds | `GetMicrovmImageBuild`, `ListMicrovmImageBuilds` | `.../builds/{buildId}` |
| Managed images | `ListManagedMicrovmImages`, `ListManagedMicrovmImageVersions` | `/2025-09-09/managed-microvm-images...` |
| Tags | `TagResource`, `UntagResource`, `ListTags` | `/2017-03-31/tags/{Resource}` |

Corrections to earlier guesses. The create verb is `RunMicrovm`, not `CreateMicrovm`. There is no `UpdateMicrovm`. Tokens are two first-class operations, the shell variant backing `microvm exec`. Builds are their own sub-resource under an image version, so the build lifecycle is observable per build, not just per image.

### Lambda Core — 5 operations

`CreateNetworkConnector`, `GetNetworkConnector`, `ListNetworkConnectors`, `UpdateNetworkConnector`, `DeleteNetworkConnector` under `/2026-04-04/network-connectors`. The model names the compute resource type generically (`ComputeResourceType: MicroVm`), which reads as connectors being shared plumbing for future Lambda compute kinds.

## Recorded corrections (2026-07-29, live service)

Facts the models could not state, learned during the fixture-recording runs.

| Fact | Detail |
|------|--------|
| Identifiers are ARNs | Image URI paths take the full image ARN as `{imageIdentifier}`; a bare name gets `400 {"message":"Invalid ARN format: …"}`. Raw colons in the path are accepted unencoded |
| VM ids | `microvm-<uuid>`, e.g. `microvm-bba53bbe-…`; VM routes take this id, not an ARN |
| Connector `ClientToken` is required | `400 {"message":"ClientToken is a required field"}` — the Smithy model marks it optional. A model-vs-service divergence, exactly what the freshness watch exists to catch |
| `allowedPorts` union serialization | `PortSpecification` is a union whose `allPorts` member targets `Unit`: serialize `{"allPorts": {}}`. A boolean gets `400 {"Message":"Expected null"}` |
| Image delete is asynchronous | `DeleteMicrovmImage` returns `200 {"imageIdentifier": …, "state": "DELETING"}` and the image lingers listable until the delete completes; a create reusing the name during that window gets `400 "already exists"` |
| Delete refused mid-build | Deleting an image whose first build is still running gets `400 {"message":"Cannot delete MicroVM image in its current state: …"}` |
| Create response is richer than the models | Live `CreateMicrovmImage` returns `id`, `buildPhaseOverrides`, `roleConfiguration` — fields absent from both the vendored and SDK models |
| `UpdateMicrovmImage` (PUT) is full-replace | Omitting `baseImageArn`/`codeArtifact` gets per-member validation errors; the PUT body must carry the full required representation, not a patch |
| `idlePolicy.autoResumeEnabled` is required | Whenever `idlePolicy` is present on `RunMicrovm` — the model marks no member of `IdlePolicy` required |
| `SHELL_INGRESS` connectors exist | Shell auth tokens require "SHELL_INGRESS network connector to be configured on the MicroVM" — a connector type entirely absent from the model's `NetworkConnectorType` enum (`VPC_EGRESS` only). The suite records the without-ingress 400 as truth; the full shell flow is future work |
| `AssociatedComputeResourceTypes` is required | For VPC_EGRESS connectors — the model marks it optional. chant's `MicrovmApp` already emits it |
| `NetworkProtocol` is required | Also for VPC_EGRESS connectors, also modeled optional — "cannot be null or empty". `MicrovmApp` emits it too |
| Image update is asynchronous | The PUT moves the image through `UPDATING`; a version PATCH during that window gets `409 "MicroVM Image is already in state: UPDATING"` |
| Image update mints a new version | The full-replace PUT triggers a rebuild: a second version ("2.0") appears after update, exactly as a rebuild-by-create does |
| The operator role is validated live at create | Connector creation assumes the role and calls EC2 immediately: a role lacking the documented ENI grants gets `400 "Encountered unauthorized operation while calling EC2 due to invalid ConnectorOperatorRole permissions"`. chant `MicrovmApp`'s operator-role shapes are the working reference |
| Delete refuses while VMs run | `400 "Cannot delete microvm image with running microvms."` — and since terminate is itself async, a delete right after terminate races the TERMINATING window |
| `OperatorRole` is required | The fourth modeled-optional-but-enforced connector member: `400 "NetworkConnectorOperatorRole is required for VPC_EGRESS connector type"` |
| Version delete is asynchronous too | An image delete racing a draining version delete gets `400 "Cannot delete MicroVM image in its current state"`; once versions drain, the same delete succeeds |
| Defaults on running VMs | Every VM carries managed default connectors (`INTERNET_EGRESS` egress, `HTTP_INGRESS` ingress — the latter another unmodeled connector type), `maximumDurationInSeconds: 28800`, endpoint `<uuid>.lambda-microvm.<region>.on.aws`, and `stateReason: "Success."` once terminated |

## Recorded corrections (2026-07-30, second session)

| Fact | Detail |
|------|--------|
| A suspended VM still issues auth tokens | `CreateMicrovmAuthToken` against a `SUSPENDED` VM returns `200` with a full `X-aws-proxy-auth` token, not a conflict. Tokens are therefore obtainable before a resume, which is the order a client wanting to wake a VM by calling it would need |
| `ResourceNotFoundException` carries `resourceId` and `resourceType` | Both `null` in practice on a missing VM. Absent from the earlier recording only because that probe hit a gateway `502` on a malformed id |
| Resume skips `PENDING` | See [lifecycle.md](lifecycle.md). The recorded transition sequences now live there |

## Error and throttle taxonomy

Also in the model, no recording run needed for shapes. `AccessDeniedException`, `ConflictException`, `ResourceConflictException`, `ResourceNotFoundException`, `InvalidParameterValueException`, `ServiceQuotaExceededException`, `ThrottlingException`, `TooManyRequestsException`, `InternalServerException`, `ServiceException`. `ThrottleReason` enumerates six reasons including `ConcurrentSnapshotCreateLimitExceeded`, which is the one QuotaGuard testing cares about. What the model cannot say is which operation returns which error when, and that mapping stays a conformance-recording target.

## Protocol notes

rest-json with versioned URI prefixes, riding the Lambda endpoint family. m80 listens on one port and dispatches by route across the three URI families. The SDK's endpoint override points the whole client at m80, which is how the KubeMicroVM operator, the `microvm` CLI with `--direct`, and any SDK consumer attach.

Errors matter as much as happy paths. The conformance suite records the real service's error codes for the standard set. Not found, conflict on double-terminate, validation failures for each enforced limit, throttling shape for the quota tests KubeMicroVM's QuotaGuard exercises. Emulating the throttling envelope is what lets their rate-limiter logic be tested offline.

## The VM endpoint

Each running VM gets an endpoint URL. m80 answers it from the same process, routed by host header or path prefix, returning a configurable stub body and honoring `X-aws-proxy-auth` against issued tokens. Suspended VMs answer the way the real service answers, which the conformance suite must record rather than guess.

## Health and introspection

`/_m80/health` reports implemented operations against the model inventory, the mudflaps convention. A `/_m80/clock` test hook advances the injected clock so lifecycle tests are deterministic and instant.
