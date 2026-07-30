# Roadmap

## M0 — Design

This repo. Docs only, local only. Exits when the operation inventory is extracted and the open questions close.

## M1 — Model extraction and suite skeleton

Done 2026-07-29. The operation inventory (24 + 5 operations across two services), state enums, error shapes, and throttle reasons are extracted from KubeMicroVM's vendored models into [api-surface.md](api-surface.md) and [lifecycle.md](lifecycle.md). The aws-sdk-go-v2 cross-check found no divergence, and `conformance/cmd/modelwatch` now re-runs that check against both vendored models on a schedule. The suite skeleton covers all 29 operations. No emulator code.

## M2 — Fixture recording

Run 2026-07-29. All ten scenarios are fixture-backed and 19 recorded corrections landed in [api-surface.md](api-surface.md), including four connector members the models mark optional and the service enforces, two connector types absent from the model's enum, and the asynchronous delete and update windows.

A second, smaller session on 2026-07-30 retired most of what the first left open. Transition order is recorded — resume goes straight back to `RUNNING` without passing through `PENDING`. A suspended VM does still issue auth tokens. `errors-not-found/get-vm-missing` re-recorded clean as `404 ResourceNotFoundException` once the case stopped probing a malformed id; the `502` it captured before survives as `.rejected-502`.

Two targets remain. The endpoint probe, running or suspended, needs the runner to address a VM's own hostname rather than the control-plane endpoint, and is unexpressible until then. The throttle probe is authored as `conformance/cmd/throttleprobe` — a concurrent `RunMicrovm` burst, scoped small and with guaranteed teardown — and has not yet been run.

## M3 — m80 itself

Go, single binary, distroless image, injected clock, in-memory store. Implement to the suite. Health endpoint reports coverage against the model inventory. Failure injection and drift levers land with the state machine, not after.

## M4 — Operator proof

KubeMicroVM on k3d pointed at m80 via endpoint override. Run their UAT suite, publish the pass matrix. This is the number that opens the codriverlabs conversation and the kit's local loop in kubemicrovm-ops M3 and M4.

## M5 — Ecosystem

chant integration (`chant emulator up` capability, the kit's local tutorials), behold demo, GHCR image, then the floci CFN module against the suite subset.

## Open questions

The name is settled. m80, checked 2026-07-29. `intentius/m80` is free on GitHub and the existing m80-named repos are an iOS label library and CP/M-80 retrocomputing projects, no collision in this space. Prior working title was squib, dropped for colliding with a 953-star Ruby project.

Endpoint behavior for running and suspended VMs. The token half is recorded; the endpoint half needs the runner to address a VM's own hostname, which it cannot do.

Whether `SUSPENDING` and `TERMINATING` are observable at all, or too brief to sample. m80 models them regardless, since a client polling faster than five seconds may well see them.

Resolved: the repo went public 2026-07-30, ahead of the M3-or-M4 question. The vendored models parse clean, carry full shapes, and agree with aws-sdk-go-v2; `modelwatch` keeps that true. Token behavior for suspended VMs is recorded. The throttle probe's shape is settled — a small concurrent `RunMicrovm` burst.
