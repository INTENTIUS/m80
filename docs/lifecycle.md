# Lifecycle

The state machines are the product. Everything else is CRUD.

## Image build

```
Creating ──▶ Building ──▶ Available
                │
                └──▶ Failed
```

Build takes a deterministic delay on the injected clock, default a few seconds of wall time when no test clock is attached, so demos feel real and tests run instant. A failure injection knob forces `Failed` for compensation testing, the same lever mudflaps grew in v0.5.0 for deploy-failure injection, which chant Ops testing needed.

Versions. A rebuild from the same name mints a new version, prior versions remain until deleted, an in-use version refuses deletion. KubeMicroVM's version pruning and generation update features exercise exactly this.

## VM lifecycle

```
Pending ──▶ Running ──▶ Suspending ──▶ Suspended
               ▲                           │
               └────────── Resuming ◀──────┘
Running | Suspended ──▶ Terminating ──▶ Terminated
```

State names are working labels until the conformance suite records the real strings. The shape is the claim.

| Behavior | Rule |
|----------|------|
| Idle suspend | `maxIdleDurationSeconds` of no endpoint traffic moves Running to Suspending on the clock |
| Suspend cap | `suspendedDurationSeconds` in Suspended moves the VM to Terminating |
| Eight hour cap | Total session life bounded at eight hours regardless of state |
| Resume | Suspended to Running restores the state marker, a monotonic counter clients can read through the endpoint stub to prove state survived |
| Terminate | Terminal. Subsequent mutations return the recorded conflict error |

Transient states settle on the injected clock with short deterministic delays, the mudflaps pattern verbatim. The clock hook advances time in tests, so a suspend-after-15-minutes policy is testable in microseconds.

## Drift levers

KubeMicroVM's drift detection and auto-suspend features watch for the service changing state underneath the CRs. m80 therefore exposes test levers that mutate state out of band. Force-suspend a VM, fail a build, terminate behind the operator's back. These levers are what make the operator's drift UAT runnable offline, and they are m80's version of mudflaps' failure injection, a feature the real service will never offer a test suite.
