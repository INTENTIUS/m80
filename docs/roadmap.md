# Roadmap

## M1 — Model extraction and suite skeleton

Done 2026-07-29. The operation inventory (24 + 5 operations across two services), state enums, error shapes, and throttle reasons are extracted from KubeMicroVM's vendored models into [api-surface.md](api-surface.md) and [lifecycle.md](lifecycle.md). The aws-sdk-go-v2 cross-check found no divergence, and `conformance/cmd/modelwatch` re-runs that check against both vendored models on a schedule. The suite skeleton covers all 29 operations. No emulator code.

## M2 — Fixture recording

Run 2026-07-29. All ten scenarios became fixture-backed and 19 recorded corrections landed in [api-surface.md](api-surface.md), including four connector members the models mark optional and the service enforces, two connector types absent from the model's enum, and the asynchronous delete and update windows.

A second, smaller session on 2026-07-30 retired most of what the first left open. Transition order is recorded: resume goes straight back to `RUNNING` without passing through `PENDING`. A suspended VM does still issue auth tokens. `errors-not-found/get-vm-missing` re-recorded clean as `404 ResourceNotFoundException` once the case stopped probing a malformed id, and the `502` it captured before survives as `.rejected-502`.

The throttle probe ran too, as `conformance/cmd/throttleprobe`: six concurrent `RunMicrovm` calls, two admitted and four rejected with `402 ServiceQuotaExceededException` against an account memory ceiling. It answered a different question than the one asked, because the memory quota fires before any concurrency throttle, so the six `ThrottleReason` values stay unobserved and m80 implements them from the model.

A third session on 2026-08-01 recorded the last unreachable surface, the per-VM endpoint. See M5.

## M3 — m80 itself

Done 2026-07-31. Go, single binary, distroless image, injected clock, in-memory store, implemented to the suite. All 29 operations answer, `/_m80/health` reports 29/29 with an empty pending list, and the conformance suite runs 100 checks with nothing skipped and nothing unimplemented. Failure injection landed with the state machines rather than after: a build can be forced to `FAILED`, a connector to any of the seven reason codes, and the account memory ceiling and request throttles are configurable.

Pointing a real client at it was the check that mattered. The AWS CLI drives the whole lifecycle through an endpoint override with dummy credentials, because m80 reads the region out of the sigv4 scope and validates no signature.

Five findings came out of pointing an implementation at fixtures that had only ever been recorded. The normalizer and the fixture corpus drifted apart three separate times, which is the failure mode to expect: a fixture mismatch is as likely to be the harness as the emulator. Two more came from the models themselves. Connector constraint violations answer a `ValidationException` the Lambda Core model does not list on any of its operations, and `ThrottleReason` exists on Lambda Core and classic Lambda but not on the MicroVMs model, so a chosen throttle reason is not expressible on the MicroVM operations at all.

## M4 — Operator proof

Done 2026-08-01. KubeMicroVM on k3d pointed at m80, running the operator's own UAT suite unmodified: 50 of 63 cases pass, and none of the thirteen failures is m80 answering differently from real AWS. Three of them are an operator bug that m80's fidelity exposes. The harness, the deviations it needed, and an account of every failure are in [standing up KubeMicroVM](kubemicrovm.md).

Three issues went upstream from what it found ([KubeMicroVM#50](https://github.com/codriverlabs/KubeMicroVM/issues/50), [#51](https://github.com/codriverlabs/KubeMicroVM/issues/51), [#52](https://github.com/codriverlabs/KubeMicroVM/issues/52)), all acknowledged by the maintainers.

## M5 — Ecosystem

The GHCR image shipped with M3 and is at v0.2.0. The model freshness watch runs weekly. The [floci subset](https://github.com/INTENTIUS/m80/blob/main/conformance/SUBSET-FLOCI.md) is exported and a floci build passes it at 26 checks.

The endpoint probe closed on 2026-08-01. It had been open since M2 because the conformance runner could only address the control plane and could only send signed requests, so a VM's own hostname was unreachable and every answer the endpoint gave was an inference. Giving a step its own base URL and headers made it recordable, and four of the nine inferences turned out to be wrong. [api-surface.md](api-surface.md) has the table.

Still open: chant integration (`chant emulator up`), the behold demo, and the floci CFN module landing upstream.

## Open questions

`TERMINATING` has never been sampled, and neither has `PENDING` on a build. `SUSPENDING` was, on 2026-08-01, by one VM in a recording where a second VM at the same poll resolution missed it. They are real and brief. m80 models all of them on the injected clock, where a test can hold them open as long as it needs.

Whether m80 should be shown to anyone yet. Tracked as [#54](https://github.com/INTENTIUS/m80/issues/54): hardened, runnable cold by someone new, and known to work. The last of those is the honest gap, since nobody outside this work has followed the docs and KubeMicroVM is still the only real consumer.

Resolved: the name (m80, checked 2026-07-29, no collision in this space; the prior working title squib was dropped for colliding with a 953-star Ruby project). The repo went public 2026-07-30. The vendored models parse clean, carry full shapes, and agree with aws-sdk-go-v2, and `modelwatch` keeps that true.
