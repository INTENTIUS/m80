# Roadmap

## M0 — Design

This repo. Docs only, local only. Exits when the operation inventory is extracted and the open questions close.

## M1 — Model extraction and suite skeleton

Done 2026-07-29. The operation inventory (24 + 5 operations across two services), state enums, error shapes, and throttle reasons are extracted from KubeMicroVM's vendored models into [api-surface.md](api-surface.md) and [lifecycle.md](lifecycle.md). The aws-sdk-go-v2 cross-check found no divergence, and `conformance/cmd/modelwatch` now re-runs that check against both vendored models on a schedule. The suite skeleton covers all 29 operations. No emulator code.

## M2 — Fixture recording

Run 2026-07-29. All ten scenarios are fixture-backed and 19 recorded corrections landed in [api-surface.md](api-surface.md), including four connector members the models mark optional and the service enforces, two connector types absent from the model's enum, and the asynchronous delete and update windows.

Three targets outlived the run and need a second, smaller session. Transition order was not captured because the harness kept only the settled response of each poll — fixed since, the runner now records the states an `until` walks through, but the answer needs a live run to collect. Token and endpoint behavior for a suspended VM had no steps at all; the token half is now a case (`auth-token-while-suspended`), the endpoint half needs the runner to address a VM's own hostname rather than the control-plane endpoint. The throttle probe was never authored. One fixture, `errors-not-found/get-vm-missing`, recorded a gateway `502` against a malformed VM id and is set aside as `.rejected-502` pending a re-record.

## M3 — m80 itself

Go, single binary, distroless image, injected clock, in-memory store. Implement to the suite. Health endpoint reports coverage against the model inventory. Failure injection and drift levers land with the state machine, not after.

## M4 — Operator proof

KubeMicroVM on k3d pointed at m80 via endpoint override. Run their UAT suite, publish the pass matrix. This is the number that opens the codriverlabs conversation and the kit's local loop in kubemicrovm-ops M3 and M4.

## M5 — Ecosystem

chant integration (`chant emulator up` capability, the kit's local tutorials), behold demo, GHCR image, then the floci CFN module against the suite subset.

## Open questions

The name is settled. m80, checked 2026-07-29. `intentius/m80` is free on GitHub and the existing m80-named repos are an iOS label library and CP/M-80 retrocomputing projects, no collision in this space. Prior working title was squib, dropped for colliding with a 953-star Ruby project.

Token and endpoint behavior for suspended VMs. Docs are silent and the first recording run did not answer it, having no step that touched a suspended VM with a token operation. The token case exists now; the endpoint probe is still unexpressible in the harness.

What the throttle probe should actually do. `ThrottleReason` enumerates six reasons and `ConcurrentSnapshotCreateLimitExceeded` is the one QuotaGuard testing cares about, but which operation to burst, at what rate, and how much account-level throttling risk is acceptable are maintainer calls, not defaults to guess.

Resolved: the repo went public 2026-07-30, ahead of the M3-or-M4 question. The vendored models parse clean, carry full shapes, and agree with aws-sdk-go-v2; `modelwatch` keeps that true.
