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
| `ServiceQuotaExceededException` answers **HTTP 402** | Payment Required, not the 429 or 400 anyone would guess. The model names the error and says nothing about its status, so this is only knowable by provoking it |
| The binding limit is memory, not VM count | `402 "The base maximum allocated memory limit has been reached for this account."` Six concurrent `RunMicrovm` on a fresh account yielded two running VMs and four rejections. At the 2048 MiB default tier that puts the account's base ceiling near 4096 MiB of allocated memory, not a VM count |
| Quota errors carry empty detail | `quotaCode`, `serviceCode`, `resourceId`, and `resourceType` are all present and all `null`. A client cannot branch on which quota was hit; only the message says |
| Concurrency throttling is masked | The burst never produced `ThrottlingException` or any `ThrottleReason`, including `ConcurrentSnapshotCreateLimitExceeded`. The memory ceiling fires first and hides it. KubeMicroVM's QuotaGuard will meet 402 long before it meets a throttle on a default account |

## Error and throttle taxonomy

Also in the model, no recording run needed for shapes. `AccessDeniedException`, `ConflictException`, `ResourceConflictException`, `ResourceNotFoundException`, `InvalidParameterValueException`, `ServiceQuotaExceededException`, `ThrottlingException`, `TooManyRequestsException`, `InternalServerException`, `ServiceException`. `ThrottleReason` enumerates six reasons including `ConcurrentSnapshotCreateLimitExceeded`, which is the one QuotaGuard testing cares about.

What the model cannot say is which operation returns which error when, and with what status. Two of those are now recorded. A terminal-state mutation is `400 ValidationException`, not either modeled conflict type. Exhausting capacity is `402 ServiceQuotaExceededException` against an account memory ceiling, reached at six concurrent `RunMicrovm` calls — and reached *instead of* any throttle, so the six `ThrottleReason` values remain unobserved and are implemented from the model alone. Provoking them would need an account whose memory quota is raised well above its concurrency limit, which is a support-ticket exercise rather than a recording one.

## Protocol notes

rest-json with versioned URI prefixes, riding the Lambda endpoint family. m80 listens on one port and dispatches by route across the three URI families. The SDK's endpoint override points the whole client at m80, which is how the KubeMicroVM operator, the `microvm` CLI with `--direct`, and any SDK consumer attach.

Errors matter as much as happy paths. The conformance suite records the real service's error codes for the standard set. Not found, conflict on double-terminate, validation failures for each enforced limit, throttling shape for the quota tests KubeMicroVM's QuotaGuard exercises. Emulating the throttling envelope is what lets their rate-limiter logic be tested offline.

## Network connectors

Five responses, four different member sets, and that is the model rather than an accident of recording. Create and Delete return a base six (`Arn`, `Name`, `Id`, `Configuration`, `OperatorRole`, `State`). Get adds `LastModified` and the state and update status members. Update adds `LastModified` and the update status but not `StateReason`. Only `NetworkConnectorSummary` — the list shape — carries `Type`. Absent means absent: a connector that has never been updated has no `LastUpdateStatus` in its recorded response at all, and emitting one as `null` diverges on every read. `ListNetworkConnectors` likewise omits `NextMarker` entirely, so a client looping until the marker is null would loop forever.

Bad requests come back two different ways, and which one is recorded.

| Kind | Error type | Body | Shape |
|---|---|---|---|
| Model constraint — list lengths, enum membership | `ValidationException` | lowercase `message` | `1 validation error detected: Value '[…]' at 'configuration.vpcEgressConfiguration.subnetIds' failed to satisfy constraint: Member must have length less than or equal to 16` |
| Service logic — the four enforced-but-modeled-optional members | `InvalidParameterValueException` | lowercase `message` | prose, e.g. `ClientToken is a required field` |
| Not found | `ResourceNotFoundException` | **capital `Message`** | `Network connector not found for: arn:aws:lambda:…:network-connector:<id>` |

Three things there are only knowable from a recording. `ValidationException` is not listed as an error of any connector operation in the Lambda Core model — the constraint layer sits in front of the service and answers it regardless. The member path in that message is camelCase (`configuration.vpcEgressConfiguration.subnetIds`) even though every wire member is PascalCase. And not-found answers with a capital `Message`, matching Lambda Core's own member style rather than the lowercase `message` Lambda Microvms uses, echoing the ARN the service built from whatever identifier arrived rather than the identifier itself.

**The constraint layer runs first, and in full.** The too-many-subnets probe sent seventeen subnets *and* omitted `ClientToken`, `OperatorRole`, `NetworkProtocol` and `AssociatedComputeResourceTypes` — four of the enforced members missing — and the service still answered about `subnetIds`. A client fixing errors one at a time sees every constraint violation before it sees the first prose message.

The seven `NetworkConnectorStateReasonCode` values (`InvalidSubnet`, `InvalidSecurityGroup`, `SubnetOutOfIPAddresses`, `InsufficientRolePermissions`, `Ec2RequestLimitExceeded`, `DisallowedByVpcEncryptionControl`, `InternalError`) are shared verbatim with `NetworkConnectorLastUpdateStatusReasonCode`. None can be provoked against real AWS on demand — you cannot ask EC2 to run a subnet out of addresses — so m80 injects them, which is the only way a consumer's error handling gets exercised at all.

## Tokens

`CreateMicrovmAuthToken` returns `authToken`, a `TokenParts` map. The model describes it as a mapping of auth token keys to values because "some token schemes require returning multiple auth headers", so the key is a header name; the recorded scheme returns exactly one, `X-aws-proxy-auth`. The value is a JWE in compact serialization — five parts, empty encrypted-key segment because the header says `alg: dir`, header carrying a `kid` UUID and `enc: A256GCM`. m80 mints that shape from random bytes and validates by table lookup rather than by decrypting; nothing should be read out of one past the header.

`expirationInMinutes` and `allowedPorts` are both required, and expiry is documented at a maximum of 60 minutes. `allowedPorts` has a minimum length of one and each member is a union — exactly one of `port`, `range` or `allPorts`. m80 enforces the port grant on the endpoint, because a token scoped to one port that opened another would pass requests the real service rejects.

`CreateMicrovmShellAuthToken` can only ever fail. The recorded response is `400 ValidationException` with "Shell access requires SHELL_INGRESS network connector to be configured on the MicroVM.", and `SHELL_INGRESS` is absent from the service model entirely — not merely unrecorded but unrepresentable, so no request exists that would make it succeed. m80 implements the rejection rather than answering 501, since the error is the operation's one observable behavior and a consumer that handles it is correctly exercised.

## The VM endpoint

Each running VM gets an endpoint URL. m80 answers it from the same process, routed by host header or by the `/_m80/vm/{microvmId}/` path prefix for callers that cannot forge a `Host`, returning a configurable stub body (`-vm-stub-body`) and honoring `X-aws-proxy-auth` against issued tokens. The default body and an `X-M80-State-Marker` header both carry the state marker, a counter that survives suspend and resume so a client can prove the VM kept its state rather than being rebuilt underneath it.

**Almost none of the endpoint's answers are recorded, and they could not have been.** The conformance runner signs and addresses control-plane requests; it has no way to call a host that is not the control plane, so recording any of this needs runner support that is not built. m80's answers:

| Situation | m80 | Basis |
|---|---|---|
| unknown endpoint host | `404` | no VM to serve |
| no or malformed token header | `401` | guess |
| token unknown, expired, or another VM's | `403` | guess |
| port outside the token's `allowedPorts` | `403` | guess |
| VM `TERMINATED` | `410` | guess |
| VM `PENDING` | `503` | guess |
| VM `SUSPENDED`, `autoResumeEnabled` | resume, then `200` | inferred |
| VM `SUSPENDED`, no `autoResumeEnabled` | `503` | guess |
| VM `RUNNING`, token good | `200` + stub body | stub |

The auto-resume row is an inference rather than a guess: a suspended VM issues tokens, which is the order a client that means to wake a VM by calling it has to work in, and `autoResumeEnabled` is the member that says whether it may. The 401/403 split is the guess most worth arguing with — a single 403 would have been safer, but a missing credential and a rejected one are different failures to a client retrying with a fresh token.

## Health and introspection

`/_m80/health` reports implemented operations against the model inventory, the mudflaps convention. A `/_m80/clock` test hook advances the injected clock so lifecycle tests are deterministic and instant.
