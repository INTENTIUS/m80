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

There is no `RESUMING` state in the enum. Whether `ResumeMicrovm` moves the VM through `PENDING` again or straight to `RUNNING` is still a recording target after the 2026-07-29 run, and it matters because KubeMicroVM's reconcilers poll these strings. The run did not answer it for a structural reason worth fixing before the next one: a step's `until` block polls to a settled state and only the matching response is kept, so every intermediate state the poll passes through is discarded. Transition *order* is invisible to a harness that records only endpoints. Answering it needs the runner to retain the distinct states an `until` observes.

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

KubeMicroVM's drift detection and auto-suspend features watch for the service changing state underneath the CRs. m80 therefore exposes test levers that mutate state out of band. Force-suspend a VM, fail a build, terminate behind the operator's back, fail a connector with a chosen reason code. These levers are what make the operator's drift UAT runnable offline, and they are m80's version of mudflaps' failure injection, a feature the real service will never offer a test suite.
