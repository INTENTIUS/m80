# Conformance

One suite, three targets. This is the piece that gets built first, before any emulator code.

## Why suite-first

Two implementations of one state machine will exist, squib in Go and a floci service module in Java. Without a shared contract they drift, and drift in an emulator is worse than absence because it teaches clients wrong behavior. The suite is also the recording instrument. Where AWS docs are silent, the suite runs against the real service once, records the answer as a fixture, and both emulators implement to the fixture.

## Shape

HTTP-level, language-agnostic, pointed at an endpoint URL. No SDK in the suite itself, raw signed requests, so it exercises the wire shapes every SDK generates from the same model.

| Target | Purpose |
|--------|---------|
| Real AWS | Record fixtures, verify the suite itself. Run rarely, costs money, needs an account |
| squib | The full suite, every operation, every lifecycle path, every error, the clock hook making time-dependent cases instant |
| floci module | The CFN-sufficient subset, tagged so the narrower scope is explicit rather than a pile of skips |

Suite cases tag which behaviors are fixture-backed against the real service and which are documented-only. A fidelity report per target falls out, the analog of mudflaps' health endpoint listing implemented paths, but externally verified.

## The KubeMicroVM layer

Their UAT suite is 63 Robot Framework cases driven through kubectl against a live EKS cluster. Pointing the operator's SDK at squib and running that suite on k3d is the external conformance prize. It tests through a real consumer's eyes, covers drift and auto-suspend behavior squib's levers exist for, and any pass rate becomes the headline number in the conversation with codriverlabs. This layer is theirs, not ours, which is what makes it credible.

## Sequencing

1. Extract the operation inventory from the vendored service model.
2. Write the suite skeleton with documented-only expectations.
3. One recording run against real AWS to convert expectations to fixtures. Budget the account and region once, keep fixtures in-repo.
4. Only then, implement squib against the suite.
5. The floci module later, against the tagged subset.
