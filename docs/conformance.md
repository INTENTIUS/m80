# Conformance

One suite, three targets. It was built before any emulator code, so m80 was implemented against a contract rather than the contract being written to describe m80.

## Why suite-first

Two implementations of one state machine exist, m80 in Go and a floci service module in Java. Without a shared contract they drift, and drift in an emulator is worse than absence because it teaches clients wrong behaviour.

The suite is also the recording instrument. Where the AWS docs are silent, it runs against the real service once, records the answer as a fixture, and both emulators implement to the fixture. That has caught m80 being confidently wrong more than once: [#42](https://github.com/INTENTIUS/m80/issues/42) recorded the per-VM endpoint and found four of nine reasonable-looking guesses did not match AWS.

## Shape

HTTP-level, language-agnostic, pointed at an endpoint URL. No SDK in the suite itself, raw signed requests, so it exercises the wire shapes every SDK generates from the same model.

| Target | Purpose | Where it stands |
|--------|---------|-----------------|
| Real AWS | Record fixtures, verify the suite itself. Runs rarely, costs money, needs an account | Recorded 2026-07-29, 07-30 and 08-01 |
| m80 | The full suite, every operation, every lifecycle path, every error | 101 checks, 0 failures, 29/29 operations |
| floci module | The CFN-sufficient subset, tagged so the narrower scope is explicit rather than a pile of skips | 26 checks, 0 failures at `-tier load-bearing` |

Cases record whether a behaviour is fixture-backed against the real service or documented-only, and coverage against the 29-operation inventory falls out of every run.

Running it, recording fixtures, the tier system, and the rejected-fixture conventions are documented in [`conformance/README.md`](https://github.com/INTENTIUS/m80/blob/main/conformance/README.md). The CFN-sufficient slice, and why each case in it exists, is in [`conformance/SUBSET-FLOCI.md`](https://github.com/INTENTIUS/m80/blob/main/conformance/SUBSET-FLOCI.md).

Every pull request runs the suite against a live m80 binary, so a divergence from a recorded fixture fails the build rather than surfacing at release time.

## The KubeMicroVM layer

Their UAT suite is 63 Robot Framework cases driven through kubectl, written for a live EKS cluster. Pointing the operator's SDK at m80 and running that suite on k3d is the external conformance prize: it tests through a real consumer's eyes, and the layer is theirs rather than ours, which is what makes it credible.

50 of 63 pass. [Standing up KubeMicroVM](kubemicrovm.md) has the harness, the matrix, and an account of every failure.
