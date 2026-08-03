# Lifecycle

The state machines are the product. Everything else is CRUD. All state strings below are the real enums, extracted 2026-07-29 from the vendored service model. The enums bound the space; the transitions between them come from recording the live service, and are marked where they do.

## Images

Three layers, each with its own state enum.

| Layer | Enum | Values |
|-------|------|--------|
| Image (the named resource) | `MicrovmImageState` | `CREATING`, `CREATED`, `CREATE_FAILED`, `UPDATING`, `UPDATED`, `UPDATE_FAILED`, `DELETING`, `DELETE_FAILED`, `DELETED` |
| Version (one build lineage) | `MicrovmImageVersionState` | `PENDING`, `IN_PROGRESS`, `SUCCESSFUL`, `FAILED`, `DELETING`, `DELETED`, `DELETE_FAILED` |
| Build (one attempt) | `BuildState` | `PENDING`, `IN_PROGRESS`, `SUCCESSFUL`, `FAILED` |

Versions also carry `MicrovmImageVersionStatus` (`ACTIVE`, `INACTIVE`), a separate axis from build progress. Architecture is `ARM_64` on `GRAVITON` only, m80 rejects anything else the way the service would.

Build takes a deterministic delay on the injected clock, default a few seconds of wall time when no test clock is attached, so demos feel real and tests run instant. A failure injection knob forces `FAILED` for compensation testing, the same lever mudflaps grew in v0.5.0 for deploy-failure injection, which chant Ops testing needed.

Deletion is asynchronous, recorded live: `DeleteMicrovmImage` answers `200` with `state: DELETING`, the image stays listable until the delete drains, and its name stays reserved (a create during the window gets `400 "already exists"`). A delete while the first build is still running is refused outright with `400 "Cannot delete MicroVM image in its current state"`. m80 models both: the DELETING window on the clock, and the mid-build refusal.

A rebuild mints a new version, prior versions remain until deleted, an in-use version refuses deletion. KubeMicroVM's version pruning and generation update features exercise exactly this, and builds being a sub-resource (`GetMicrovmImageBuild`) means per-attempt progress is observable, which their image build logs feature reads.

## VMs

`MicrovmState`: `PENDING`, `RUNNING`, `SUSPENDING`, `SUSPENDED`, `TERMINATING`, `TERMINATED`. VM ids are `microvm-<uuid>` (recorded; the working guess of `mv-…` was wrong). The terminal-state path is recorded and surprising: suspending a TERMINATED VM returns `400 ValidationException` ("has been terminated and its state cannot be changed"), not a ConflictException — the modeled conflict type appears reserved for other collisions. `TerminateMicrovm` on a live VM returns `200 {}` with the state settling through TERMINATING.

```
PENDING ──▶ RUNNING ──▶ SUSPENDING ──▶ SUSPENDED
               ▲                           │
               └───────── (resume) ◀───────┘
RUNNING | SUSPENDED ──▶ TERMINATING ──▶ TERMINATED
```

There is no `RESUMING` state in the enum, and **`ResumeMicrovm` does not pass back through `PENDING`**. Recorded 2026-07-30: a five-second poll across a full suspend and resume cycle observed `SUSPENDED` then `RUNNING` with nothing between. The same poll, at the same resolution, did catch `PENDING` on the initial launch, so this is a real difference in the two paths rather than a sampling artifact. KubeMicroVM's reconcilers poll these strings, so a resumed VM going straight to `RUNNING` is the behavior m80 implements.

Observed sequences, all at poll resolution and therefore a lower bound — a state briefer than the interval would not appear:

| Transition | Observed |
|------------|----------|
| Launch | `PENDING` → `RUNNING` |
| Suspend | `RUNNING` → `SUSPENDED` |
| Resume | `SUSPENDED` → `RUNNING` |
| Terminate | `RUNNING` → `TERMINATED` |
| Build | `IN_PROGRESS` → `SUCCESSFUL` |

`SUSPENDING` was sampled on 2026-08-01, in the `vm-endpoint` recording: a five-second poll across an explicit suspend caught `SUSPENDING` then `SUSPENDED`. The same poll on the other VM in the same run caught only `RUNNING` then `SUSPENDED`, so it is real and brief enough to miss. `TERMINATING` and `PENDING` on a build are still unsampled and presumably the same. m80 models them on the injected clock, where a test can hold them open as long as it needs; treating them as unreachable because one recording missed them would be the wrong lesson.

Recording these at all needed a harness change. A step's `until` block polls to a settled state and originally kept only the matching response, discarding every intermediate. The runner now retains the distinct states a poll walks through and writes them to the fixture's `.meta.json` as `observedStates` — recorded truth, never asserted, since an instant-settle emulator walks a shorter path and is still conformant on every observable state.

| Behavior | Rule |
|----------|------|
| Idle suspend | `maxIdleDurationSeconds` of no endpoint traffic moves `RUNNING` to `SUSPENDING` on the clock |
| Suspend cap | `suspendedDurationSeconds` in `SUSPENDED` moves the VM to `TERMINATING` |
| Eight hour cap | Total session life bounded at eight hours regardless of state |
| Resume | `SUSPENDED` back to `RUNNING` restores the state marker, a monotonic counter clients can read through the endpoint stub to prove state survived |
| Terminate | Terminal. Subsequent mutations return `400 ValidationException`, recorded — neither modeled conflict type |

Transient states settle on the injected clock with short deterministic delays, the mudflaps pattern verbatim. The clock hook advances time in tests, so a suspend-after-15-minutes policy is testable in microseconds.

## Network connectors

`NetworkConnectorState`: `PENDING`, `ACTIVE`, `INACTIVE`, `FAILED`, `DELETING`, `DELETE_FAILED`, with a reason-code enum (`InvalidSubnet`, `InvalidSecurityGroup`, `SubnetOutOfIPAddresses`, `InsufficientRolePermissions`, `Ec2RequestLimitExceeded`, `DisallowedByVpcEncryptionControl`, `InternalError`) shared by state and last-update status. The reason codes are a gift, each one is a realistic failure m80 can inject without inventing anything, and KubeMicroVM's MicroVMNetwork reconciler has visible handling to exercise.

## Drift levers

KubeMicroVM's drift detection and auto-suspend features watch for the service changing state underneath the CRs, so m80 can fail things the real service would only fail by bad luck. This is m80's version of mudflaps' failure injection, a feature the real service will never offer a test suite.

Two levers: `images.Service.FailNextBuild` forces the next build of a named image to `FAILED`, and `connectors.Service.FailNext` settles the next connector of a given name into `FAILED` carrying any of the seven reason codes. Neither can be provoked against real AWS on demand — you cannot ask EC2 to run a subnet out of addresses — which is the whole reason they exist.

Both were Go-only until [#56](https://github.com/INTENTIUS/m80/issues/56). A test that imported m80 could drive them; a suite pointed at the container could not reach them at all, which is exactly backwards, since a container is what a consumer tests against.

They now have an HTTP surface, off unless asked for:

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80 -enable-injection

curl -X POST localhost:4290/_m80/inject -d '{"target":"build","name":"doomed"}'
curl -X POST localhost:4290/_m80/inject \
     -d '{"target":"connector","name":"egress","reasonCode":"SubnetOutOfIPAddresses"}'
```

Three things about that shape are deliberate.

It is keyed by **name, not ARN**. A lever arms before the resource exists — that is what "the next build of this image fails" means — so at the moment of arming there is no ARN to name it with.

The response carries **`"injected": true`**. A state m80 reached on its own never carries it, so a consumer asserting on that field cannot mistake an injected `FAILED` for one the emulator produced by its own rules.

It is **off by default**. Nothing under `/_m80/` is signed, so anything that can reach the port can arm a lever; a flag is the consent, the same posture `-serve-sts` takes. Without the flag the route is still registered, and answers 404 with a message naming the flag — a bare 404 is indistinguishable from a typo in the path.

The gap was originally thought to block the offline drift run behind [#18](https://github.com/INTENTIUS/m80/issues/18). It did not: drift is provoked with ordinary `SuspendMicrovm` and `TerminateMicrovm` calls, which any client can make, and the [KubeMicroVM harness](kubemicrovm.md) does exactly that. It was real for a different reason — failure paths are what a consumer most needs a test target for, and they were the ones m80 could not be asked to take.
