# When m80 has not seen it

Every wire shape in m80 comes from a recording against live AWS or from the service model. Some requests reach neither: the model permits them and no recording covers them. This page is the rule for what m80 does then, and the complete list of where it has had to.

It exists because the alternative is a consumer discovering the answer. An emulator that guesses is not the problem — an emulator that guesses *invisibly* is.

## The rule

In order. The first that applies, wins.

**1. A recording decides it.** Not negotiable and not interesting; it is what the conformance suite checks.

**2. The model decides it.** An enum member, a required field, a status the model pins. `TERMINATING` was never sampled at a five-second poll, and it is in `MicrovmState`, so m80 goes through it rather than jumping — the model is evidence, and a faster client than the recorder would see it.

**3. A recorded neighbour decides it, by a rule already observed.** Not "something similar looked like this". The endpoint answers a `PENDING` VM the way it answers every other unavailable VM, because *every unavailable state that was recorded* answers the same way — the rule is the observation, not the resemblance.

**4. Otherwise, prefer the answer that cannot invent data.** Ranked:

- Omit an optional member rather than invent its value.
- Reuse a recorded string rather than coin one.
- Be idempotent rather than invent an error type.

**5. If an honest answer would put invented data on the wire, refuse with 501 and say why.** Not the modelled error shape — a modelled error is m80 claiming the service said no. A 501 is m80 saying it does not know, which is a different and more useful statement.

**The rule behind the rule: never invent a value a consumer could branch on.** That is what [#42](https://github.com/INTENTIUS/m80/issues/42) punished. Omitting, refusing and being idempotent are all recoverable — a client that hits them learns something true. A wrong invented value is not recoverable, because a client builds on it.

## Where it has come up

Seven places. Each is marked in the source with `UNRECORDED: <id> — <disposition>` at the line that decides, and `internal/unrecorded_test.go` fails if this table and those markers disagree.

| Id | Situation | Disposition | What m80 does |
|----|-----------|-------------|----------------|
| `sts-other-actions` | Any STS action but `GetCallerIdentity` | refuse | 501 with a message saying m80 is a shim, not an STS emulator, and that the action will not be added by guessing |
| `terminating-visible` | Whether `TERMINATING` is observable | follow-the-model | Goes through it. Never sampled at a five-second poll; it is in the enum |
| `endpoint-pending-vm` | Calling a `PENDING` VM's endpoint | extrapolate | 502 with an empty body, as every recorded unavailable state answers |
| `endpoint-shell-token` | A shell token presented to the HTTP endpoint | extrapolate | Refused as a token that does not match. Recording it needs `SHELL_INGRESS` on the image |
| `suspend-non-running` | Suspending a VM that is pending, suspending, suspended or terminating | safest-of-two | 200, idempotent. The model offers conflict types; inventing one would make a reconciler's retry an error |
| `cap-terminated-reason` | `stateReason` on a VM the idle or session cap ended | reuse-recorded | `"Success."`, the recorded string for a clean terminate. The service's own wording for a capped end was never recorded |
| `connector-update-reason` | `lastUpdateStatusReason` after an update that changed something | omit | Plain success, no reason member. Only the no-change wording was ever recorded |

## What to do with this list

If you are testing a consumer against m80 and your assertion touches a row here, that assertion is about m80's judgement rather than about AWS. Either avoid it, or record the real behaviour and delete the row — the second is strictly better and is how three of these got shorter already.

If you are adding an operation to m80 and find yourself about to decide one of these, add a row. A guess with a name and a reason is a debt; a guess without one is a defect waiting to be attributed to AWS.
