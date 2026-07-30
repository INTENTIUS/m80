# Roadmap

## M0 — Design

This repo. Docs only, local only. Exits when the operation inventory is extracted and the open questions close.

## M1 — Model extraction and suite skeleton

Partially done 2026-07-29. The operation inventory (24 + 5 operations across two services), state enums, error shapes, and throttle reasons are extracted from KubeMicroVM's vendored models into [api-surface.md](api-surface.md) and [lifecycle.md](lifecycle.md). Remaining: cross-check against aws-sdk-go-v2's model for divergence, then write the conformance suite skeleton. No emulator code.

## M2 — Fixture recording

One budgeted run against the real service, now much smaller than first scoped since the model gave up shapes, enums, and error taxonomy for free. What only the live service can answer: transition order (does resume pass through `PENDING`), which operation returns which error when, suspended-endpoint and token behavior, the throttle envelope in practice. This run retires every remaining recording target in [lifecycle.md](lifecycle.md).

## M3 — m80 itself

Go, single binary, distroless image, injected clock, in-memory store. Implement to the suite. Health endpoint reports coverage against the model inventory. Failure injection and drift levers land with the state machine, not after.

## M4 — Operator proof

KubeMicroVM on k3d pointed at m80 via endpoint override. Run their UAT suite, publish the pass matrix. This is the number that opens the codriverlabs conversation and the kit's local loop in kubemicrovm-ops M3 and M4.

## M5 — Ecosystem

chant integration (`chant emulator up` capability, the kit's local tutorials), behold demo, GHCR image, then the floci CFN module against the suite subset.

## Open questions

The name is settled. m80, checked 2026-07-29. `intentius/m80` is free on GitHub and the existing m80-named repos are an iOS label library and CP/M-80 retrocomputing projects, no collision in this space. Prior working title was squib, dropped for colliding with a 953-star Ruby project.

Whether the repo goes public at M3 or M4. The operator-proof number is the better launch, but earlier visibility might recruit codriverlabs as design partners.

Token and endpoint behavior for suspended VMs. Docs are silent, M2 records it.

Whether the vendored service model in KubeMicroVM is complete and current, or whether the SDK's own model diverges. Half answered, the vendored models parse clean and carry full shapes. The aws-sdk-go-v2 cross-check remains.
