# Lifecycle

The state machines are the product. Everything else is CRUD. All state strings below are the real enums, extracted 2026-07-29 from the vendored service model. Transitions between them are still conformance-recording targets, the enums bound the space but do not order it.

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

`SUSPENDING` and `TERMINATING` are in the enum but were never sampled, as were `PENDING` on a build. They are presumably real and brief. m80 models them on the injected clock, where a test can hold them open as long as it needs; treating them as unreachable because one recording missed them would be the wrong lesson.

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

Two levers exist today, and both are Go APIs rather than endpoints: `images.Service.FailNextBuild` forces the next build of a named image to `FAILED`, and `connectors.Service.FailNext` settles the next connector of a named connector into `FAILED` carrying any of the seven reason codes. Neither can be provoked against real AWS on demand — you cannot ask EC2 to run a subnet out of addresses — which is the whole reason they exist.

Being Go-only bounds what they are good for. A test that imports m80 can drive them; a UAT pointed at the container cannot reach them at all, so the offline drift run that motivates them ([#18](https://github.com/INTENTIUS/m80/issues/18)) needs an HTTP surface that is not built yet. Suspending or terminating a VM behind an operator's back needs no lever in any case — those are ordinary API calls that any test can make.
