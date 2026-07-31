# Roadmap

## M0 — Design

This repo. Docs only, local only. Exits when the operation inventory is extracted and the open questions close.

## M1 — Model extraction and suite skeleton

Done 2026-07-29. The operation inventory (24 + 5 operations across two services), state enums, error shapes, and throttle reasons are extracted from KubeMicroVM's vendored models into [api-surface.md](api-surface.md) and [lifecycle.md](lifecycle.md). The aws-sdk-go-v2 cross-check found no divergence, and `conformance/cmd/modelwatch` now re-runs that check against both vendored models on a schedule. The suite skeleton covers all 29 operations. No emulator code.

## M2 — Fixture recording

Run 2026-07-29. All ten scenarios are fixture-backed and 19 recorded corrections landed in [api-surface.md](api-surface.md), including four connector members the models mark optional and the service enforces, two connector types absent from the model's enum, and the asynchronous delete and update windows.

A second, smaller session on 2026-07-30 retired most of what the first left open. Transition order is recorded — resume goes straight back to `RUNNING` without passing through `PENDING`. A suspended VM does still issue auth tokens. `errors-not-found/get-vm-missing` re-recorded clean as `404 ResourceNotFoundException` once the case stopped probing a malformed id; the `502` it captured before survives as `.rejected-502`.

The throttle probe ran too, as `conformance/cmd/throttleprobe`: six concurrent `RunMicrovm` calls, two admitted and four rejected with `402 ServiceQuotaExceededException` against an account memory ceiling. It answered a different question than the one asked — the memory quota fires before any concurrency throttle, so the six `ThrottleReason` values stay unobserved and m80 implements them from the model.

One target remains. The endpoint probe, running or suspended, needs the runner to address a VM's own hostname rather than the control-plane endpoint, and is unexpressible until then.

## M3 — m80 itself

Done 2026-07-31. Go, single binary, distroless image, injected clock, in-memory store, implemented to the suite. All 29 operations answer; `/_m80/health` reports 29/29 with an empty pending list, and the conformance suite runs 71 checks with nothing skipped and nothing unimplemented. Failure injection landed with the state machines rather than after: a build can be forced to `FAILED`, a connector to any of the seven reason codes, and the account memory ceiling and request throttles are configurable.

Pointing a real client at it was the check that mattered. The AWS CLI drives the whole lifecycle through an endpoint override — create an image, poll it to `SUCCESSFUL`, run a VM, suspend it, resume it, mint an auth token, tag the image — with dummy credentials, because m80 reads the region out of the sigv4 scope and validates no signature.

Five findings came out of pointing an implementation at fixtures that had only ever been recorded. The normalizer and the fixture corpus drifted apart three separate times, which is the failure mode to expect: a fixture mismatch is as likely to be the harness as the emulator. Two more came from the models themselves — connector constraint violations answer a `ValidationException` the Lambda Core model does not list on any of its operations, and `ThrottleReason` exists on Lambda Core and classic Lambda but not on the MicroVMs model, so a chosen throttle reason is not expressible on the MicroVM operations at all.

## M4 — Operator proof

KubeMicroVM on k3d pointed at m80 via endpoint override. Run their UAT suite, publish the pass matrix. This is the number that opens the codriverlabs conversation and the kit's local loop in kubemicrovm-ops M3 and M4.

## M5 — Ecosystem

chant integration (`chant emulator up` capability, the kit's local tutorials), behold demo, GHCR image, then the floci CFN module against the suite subset.

## Open questions

The name is settled. m80, checked 2026-07-29. `intentius/m80` is free on GitHub and the existing m80-named repos are an iOS label library and CP/M-80 retrocomputing projects, no collision in this space. Prior working title was squib, dropped for colliding with a 953-star Ruby project.

Endpoint behavior for running and suspended VMs. The token half is recorded; the endpoint half needs the runner to address a VM's own hostname, which it cannot do.

Whether `SUSPENDING` and `TERMINATING` are observable at all, or too brief to sample. m80 models them regardless, since a client polling faster than five seconds may well see them.

Resolved: the repo went public 2026-07-30, ahead of the M3-or-M4 question. The vendored models parse clean, carry full shapes, and agree with aws-sdk-go-v2; `modelwatch` keeps that true. Token behavior for suspended VMs is recorded. The throttle probe's shape is settled — a small concurrent `RunMicrovm` burst.
