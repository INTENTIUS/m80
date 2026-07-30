# Conformance suite

One suite, three targets ([docs/conformance.md](../docs/conformance.md)). Cases and fixtures are JSON; the runner is Go, for sigv4.

```sh
# Against an m80 instance (dummy credentials are automatic)
go run ./conformance/cmd/conformance -endpoint http://localhost:4290 -poll-timeout 15

# The floci CFN subset only
go run ./conformance/cmd/conformance -endpoint http://localhost:4566 -tags subset:floci -poll-timeout 15

# Recording fixtures against real AWS (env credentials, real params)
AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… \
go run ./conformance/cmd/conformance -endpoint https://lambda.us-east-1.amazonaws.com -record \
  -param buildRoleArn=arn:aws:iam::<account>:role/<build-role> \
  -param codeArtifactUri=s3://<bucket>/<key>
```

Scenario files live in `cases/`, ordered by filename. Each is self-contained: it creates what it needs and cleans up. `params` in a scenario are template defaults so emulator runs need no flags; `-param` overrides them for recording. Steps poll with `until` where a state must settle, with timeouts sized for real AWS (emulators settle in milliseconds, the poll exits on first match).

Those AWS-sized timeouts are why `-poll-timeout` exists. A target that does not model a state never reaches it, so the poll runs the case's full timeout — 600s for an image update, 2700s for a build — before the step can fail. A whole floci run takes ten minutes of which almost all is waiting; `-poll-timeout 15` makes the same run finish in sixteen seconds with identical results. Recording runs leave it unset, since there the long timeouts are load-bearing.

Outcomes: `pass`, `fail`, `unimplemented` (HTTP 501, valid for a target under construction), `skipped` (an earlier step in the scenario did not pass), `error`. The report ends with coverage against `inventory.json`, the 29-operation list generated from the verified service models. A fixture in `fixtures/<scenario>/<step>.json` upgrades a case from documented-only to fixture-backed: the response must then equal the fixture after normalization (accounts, ARNs, timestamps, UUIDs, short AWS resource ids, regions, and token-named fields are redacted on both sides).

## Fixtures and their sidecars

`<step>.meta.json` next to each fixture carries what the body cannot. `status` and `errorType` are asserted — they are the wire facts error mapping depends on. `observedStates` is recorded truth only, never asserted: it lists the distinct states an `until` poll walked through, so the live service's transition order survives a recording run instead of being discarded in favor of the settled response. An emulator that settles instantly walks a shorter path and is still conformant on every observable state, so gating on it would fail floci by design.

`region` is a built-in param, set from `-region`, and scenario params may reference each other — so a case spells a base image ARN once as `arn:aws:lambda:${region}:aws:microvm-image:al2023-1` and it follows whatever region the run signs for. Pinning a region in a param made every case silently wrong against any other one, since a correct implementation scopes its managed-image catalog by region and rejects a base image from elsewhere.

Scenario `params` defaults must reproduce the *normalized* shape of whatever was recorded, since fixture comparison covers echoed request values. `codeArtifactUri` defaults to a bucket named like the recording one (`m80-conformance-<account>-use2`) for exactly this reason: the account digits flatten to `ACCOUNT` on both sides, so an emulator echoing the default matches a live recording made against the real bucket. A default that does not normalize to the recorded string fails every target, correct ones included.

## Checking lists that accumulate

Some collections grow with an account's whole history — terminated VMs stay in `ListMicrovms` apparently forever — so a recorded list is a photograph of one account at one moment and can never equal a fresh target's. Dropping those steps to a bare status check would throw away the part that matters, which is whether the target returns the members a client reads.

`expect.itemShape` checks membership without checking values:

```json
"expect": {
  "status": 200,
  "itemShape": {
    "path": "items",
    "required": ["imageArn", "imageVersion", "microvmId", "startedAt", "state"],
    "exact": true,
    "minItems": 1
  }
}
```

`exact` rejects members outside `required` as well as missing ones, because sparse bodies and over-full ones are both real divergences — an emulator returning half the members of a summary is the commonest of all. `minItems` guards against a target that returns an empty list and satisfies every member check vacuously.

## Rejected fixtures

A fixture renamed to `<step>.json.rejected-<reason>` is a recording the suite refuses to treat as truth, because it captured the recording *account* rather than the service. They are kept rather than deleted: each one is evidence, and each cost a live session.

`rejected-502` — the probe used the pre-recording guess `mv-…` for a VM id. Real ids are `microvm-<uuid>`, and the API gateway answered the malformed path with an nginx `502` HTML page. Re-recorded clean once the case used a well-formed id.

`rejected-account-iam` — three not-found probes against a nonexistent image ARN came back `403 AccessDeniedException` rather than a not-found, because the recording account's IAM policy is resource-scoped and the invented ARN falls outside it. The bodies name the caller (`user/alex`) and use a capital-M `Message`, both tells that the response came from the IAM layer rather than the service. No emulator can reproduce them, m80 does not evaluate IAM by design, and the cases' own `expect` blocks say `ResourceNotFoundException` — so the recording contradicts the case's intent. Re-recording these needs an account whose policy covers `lambda:*` on `*`.

`rejected-account-history` — `ListMicrovms` returns every VM the account has ever run, terminated ones included and apparently forever. A recorded list is therefore a photograph of one account at one moment and can never equal a fresh target's. List steps over resources that accumulate need a different check than full equality.
