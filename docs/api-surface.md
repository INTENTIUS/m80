# API surface

## Source of truth

The MicroVM API ships as an SDK service model. Three copies are available and must agree.

| Source | Use |
|--------|-----|
| `service-2.json` vendored in KubeMicroVM's `operator-aws-client` | Machine-readable operation list, shapes, paginators. What the inventory was extracted from |
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
| `SHELL_INGRESS` connectors exist | Shell auth tokens require "SHELL_INGRESS network connector to be configured on the MicroVM" — a connector type entirely absent from the model's `NetworkConnectorType` enum (`VPC_EGRESS` only). The suite records the without-ingress 400 as truth, and no request exists that would make the operation succeed |
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
| The binding limit is memory, not VM count | `402 "The base maximum allocated memory limit has been reached for this account."` Six concurrent `RunMicrovm` on a fresh account yielded two running VMs and four rejections. At the 2048 MiB default tier that puts the account's base ceiling near 4096 MiB of allocated memory, not a VM count. The ceiling has since become directly readable — Service Quotas "Max allocated MicroVM memory" (lambda, `L-CD1C0CC4`); a fresh account read 8 GB there on 2026-08-06, so the base varies per account |
| Quota errors carry empty detail | `quotaCode`, `serviceCode`, `resourceId`, and `resourceType` are all present and all `null`. A client cannot branch on which quota was hit; only the message says |
| Concurrency throttling is masked | The burst never produced `ThrottlingException` or any `ThrottleReason`, including `ConcurrentSnapshotCreateLimitExceeded`. The memory ceiling fires first and hides it. KubeMicroVM's QuotaGuard will meet 402 long before it meets a throttle on a default account |

## Recorded corrections (2026-08-01, the VM endpoint)

The per-VM endpoint had never been recorded, because the conformance runner addressed the control plane and signed everything it sent. Giving a step its own `baseURL` and headers ([#42](https://github.com/INTENTIUS/m80/issues/42)) made it reachable, and four of m80's nine guesses were wrong.

| Fact | Detail |
|------|--------|
| A missing token and an unparseable one are the same failure | Both `403` with the plain-text body `Request missing authentication`. m80 answered `401` for a missing header, on the reasoning that a client retrying with a fresh token wants them apart. AWS does not agree |
| A token for the wrong VM is a different failure | `403 Token authentication failed`. This is the message the KubeMicroVM UAT reports on four of its cases, which confirms those curls reach real AWS rather than m80 |
| An unknown endpoint hostname answers as a mismatched token would | `403 Token authentication failed`, not a `404`. The host names no VM, so no token can match it |
| **`allowedPorts` does not gate the endpoint** | A token granting only port 8080 served `https://<endpoint>/` — port 443 — with `200`. The grant is validated at issue time and then not enforced on the hostname the control plane hands out. m80 enforced it, which would have failed requests AWS answers |
| A terminated VM answers `502` with an empty body | Not the `410` m80 guessed. So does a suspended VM whose idle policy does not enable auto-resume, where m80 guessed `503` |
| Calling a suspended VM's endpoint does wake it | With `autoResumeEnabled`, the call returns `200` and `GetMicrovm` reads `RUNNING` immediately after. The one guess that held |
| Endpoint bodies are plain text | No modeled error type, no JSON envelope. It is a proxy in front of the VM, not the control plane |

A successful call returns whatever the image serves — in the recording, the `code.zip` app's `{"path":"/","status":"ok","ts":…}`. That body is the customer's, not the service's, so its fixtures are set aside as `.image-owned-body` and those steps assert status only.

## Error and throttle taxonomy

Also in the model, no recording run needed for shapes. `AccessDeniedException`, `ConflictException`, `ResourceConflictException`, `ResourceNotFoundException`, `InvalidParameterValueException`, `ServiceQuotaExceededException`, `ThrottlingException`, `TooManyRequestsException`, `InternalServerException`, `ServiceException`. `ThrottleReason` enumerates six reasons including `ConcurrentSnapshotCreateLimitExceeded`, which is the one QuotaGuard testing cares about.

What the model cannot say is which operation returns which error when, and with what status. Two of those are now recorded. A terminal-state mutation is `400 ValidationException`, not either modeled conflict type. Exhausting capacity is `402 ServiceQuotaExceededException` against an account memory ceiling, reached at six concurrent `RunMicrovm` calls — and reached *instead of* any throttle, so the six `ThrottleReason` values remain unobserved and are implemented from the model alone. Provoking them would need an account whose memory quota is raised well above its concurrency limit, which is a support-ticket exercise rather than a recording one.

## Throttles and quotas

Two behaviors, and the difference between them decides the defaults.

The **account memory ceiling is recorded**, so it is on by default at the recorded ceiling of 4096 MiB (`-max-account-memory-mib`). Six concurrent `RunMicrovm` calls on a fresh account left two VMs running at the 2048 MiB default tier and rejected four with `402 ServiceQuotaExceededException`. Terminating a VM gives its memory back, so the ceiling bounds live allocation rather than lifetime count.

**Throttling was never observed at all** — the memory ceiling fires first and hides it — so every throttle is implemented from the model and is off by default. An emulator that throttles by surprise is worse than one that never does. `-throttle-requests-per-interval` arms it.

The throttle's wire shape depends on which service the operation belongs to, and that is the model rather than a choice:

| Family | Error type | Reason member |
|---|---|---|
| Lambda Microvms (`/2025-09-09/`) | `ThrottlingException`, 429 | none — the model's `TooManyRequestsException` carries only `Type` and `message` |
| Lambda Core (`/2026-04-04/`) and tags (`/2017-03-31/`) | `TooManyRequestsException`, 429 | `Reason`, a `ThrottleReason` |

So a chosen `ThrottleReason` is observable on the connector and tags operations and is **simply not expressible** on the MicroVM ones. `ConcurrentSnapshotCreateLimitExceeded` — the value QuotaGuard testing cares about — names a limit on image builds, but `CreateMicrovmImage` is a MicroVM-family operation, so `-max-concurrent-snapshot-creates` names the reason in the message rather than in a member that does not exist.

## Protocol notes

rest-json with versioned URI prefixes, riding the Lambda endpoint family. m80 listens on one port and dispatches by route across the three URI families. The SDK's endpoint override points the whole client at m80, which is how the KubeMicroVM operator, the `microvm` CLI with `--direct`, and any SDK consumer attach.

Errors matter as much as happy paths. The conformance suite records the real service's error codes for the standard set. Not found, conflict on double-terminate, validation failures for each enforced limit, throttling shape for the quota tests KubeMicroVM's QuotaGuard exercises. Emulating the throttling envelope is what lets their rate-limiter logic be tested offline.

## Tags

`TagResource`, `UntagResource` and `ListTags` ride the classic Lambda tags routes at `/2017-03-31/tags/{Resource}` — a third path prefix on the same listener, belonging to neither MicroVM URI family. `Resource` is a whole ARN in one path segment, which works because every taggable MicroVM-family ARN is colon-separated with no slash in it.

Both mutations answer `204` with an empty body; the recorded fixtures for those steps are zero-byte files, which is what no content looks like on disk. `ListTags` answers `200` with a `Tags` map, and an untagged resource gets an empty object rather than `null`, so a client can index it without a nil check. `TagKeys` on untag rides the query string as `tagKeys`; the SDK repeats the parameter and m80 also accepts it comma-separated, since neither costs anything.

Tagging merges rather than replaces — it adds and overwrites the keys it names and leaves the rest alone, which is what untag exists for — and untag is idempotent, which is what a reconciler converging on a desired tag set needs.

Tags are stored by the package that owns the resource rather than in one central map. A tag set belongs to the thing it is on: an image's tags have to appear in the image's own responses, and a copy kept elsewhere would be a second truth that drifts the first time anything reads the wrong one. VM and connector ARNs are taggable too, but no recorded response for either carries a `tags` member, so theirs are stored and never surfaced — inventing one to show them would diverge on every read.

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

`expirationInMinutes` and `allowedPorts` are both required, and expiry is documented at a maximum of 60 minutes. `allowedPorts` has a minimum length of one and each member is a union: exactly one of `port`, `range` or `allPorts`. m80 validates that at issue time, because the control plane does reject a malformed grant. It does not enforce the grant at the endpoint, because the 2026-08-01 recording showed a token granting only port 8080 serving port 443. m80 used to enforce it, and was rejecting requests real AWS answers.

`CreateMicrovmShellAuthToken` can only ever fail. The recorded response is `400 ValidationException` with "Shell access requires SHELL_INGRESS network connector to be configured on the MicroVM.", and `SHELL_INGRESS` is absent from the service model entirely — not merely unrecorded but unrepresentable, so no request exists that would make it succeed. m80 implements the rejection rather than answering 501, since the error is the operation's one observable behavior and a consumer that handles it is correctly exercised.

## The VM endpoint

Each running VM gets an endpoint URL. m80 answers it from the same process, routed by host header or by the `/_m80/vm/{microvmId}/` path prefix for callers that cannot forge a `Host`, returning a configurable stub body (`-vm-stub-body`) and honoring `X-aws-proxy-auth` against issued tokens. The default body and an `X-M80-State-Marker` header both carry the state marker, a counter that survives suspend and resume so a client can prove the VM kept its state rather than being rebuilt underneath it.

Every answer it gives was recorded against the live service on 2026-08-01, which needed the conformance runner to gain the ability to address a host that is not the control plane and to send a request it does not sign. Before that the endpoint was unreachable from the suite and all nine answers were inferences; four were wrong. The corrections table above has the detail.

| Situation | Answer |
|---|---|
| No token, or one that cannot be parsed | `403 Request missing authentication` |
| A token for another VM, or a hostname naming none | `403 Token authentication failed` |
| A token that does not grant the port | `200`, since `allowedPorts` does not gate this endpoint |
| VM `SUSPENDED` with `autoResumeEnabled` | `200`, and the VM reads `RUNNING` immediately after |
| VM `SUSPENDED` without it, or `TERMINATED` | `502` with an empty body |
| VM `RUNNING`, token good | `200` and whatever the image serves |

Two situations remain unrecorded because nothing reaches them. A `PENDING` VM answers as an unavailable one, on the grounds that every unavailable case that was observed answers that way. A shell token presented to the HTTP endpoint is refused as a non-matching token, since recording it needs `SHELL_INGRESS` on the image and no request exists that produces one.

## Health and introspection

`/_m80/health` reports implemented operations against the model inventory, the mudflaps convention, plus the regions the store has been asked about. `/_m80/vm/{microvmId}/` reaches a VM's endpoint stub without forging a `Host` header. `POST /_m80/inject` arms a failure — a build that settles `FAILED`, or a connector that settles `FAILED` with one of the seven reason codes — and answers 404 naming the flag unless m80 was started with `-enable-injection`. See [Lifecycle](lifecycle.md#drift-levers).

There is no clock endpoint. Transitions run on an injected clock and Go tests advance it directly, but nothing exposes that over HTTP, so a black-box client cannot skip a delay — it waits, or it starts m80 with a shorter `-build-delay`. An earlier draft of this page described a `/_m80/clock` hook that was never built.
