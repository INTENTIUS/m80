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

`region` and `account` are built-in params, and scenario params may reference each other — so a case spells a base image ARN once as `arn:aws:lambda:${region}:aws:microvm-image:al2023-1` and it follows whatever the run signs for. `region` comes from `-region`; `account` defaults to m80's own `000000000000` and a recording run passes `-param account=<real>`.

Both exist because pinning either one makes a case silently wrong elsewhere. A pinned region breaks against a correct implementation, which scopes its managed-image catalog by region and rejects a base image from another. A pinned account is worse: an ARN carrying the `123456789012` placeholder names a *foreign* account, and live AWS answers that with `403 AccessDenied` rather than a not-found — correctly, since cross-account access needs a resource-based policy regardless of the caller's own permissions.

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

## Side probes in the middle of a scenario

A failing step halts the rest of its scenario, because whatever the later steps assume about the target's state is no longer trustworthy. An unimplemented one halted it too, which was wrong for a step nothing downstream depends on.

`CreateMicrovmAuthToken` sits between `suspend` and `resume` in `vm-suspend-resume` for a good reason — that is the only place a live recording can observe a token issued against a suspended VM — but while it was unimplemented it took `ResumeMicrovm` off the coverage report with it, and the operation had nothing wrong with it.

`"optional": true` on a step exempts it from halting the scenario **when the target answers 501**:

```json
{
  "name": "auth-token-while-suspended",
  "operation": "CreateMicrovmAuthToken",
  "optional": true,
  ...
}
```

It exempts nothing else. A step that genuinely fails still halts its scenario however it is marked. Do not mark a step that carries `capture`: the vars it would have set go missing and the failure resurfaces several steps later, a long way from its cause.

## The floci subset

The `subset:floci` slice is the CFN-sufficient acceptance gate, meant to be run from another repo's CI against any endpoint. Its invocation, and a per-case map of which CloudFormation behavior each step backs, are in [SUBSET-FLOCI.md](SUBSET-FLOCI.md).

```sh
just subset-floci http://localhost:4566
```

## Measuring a target that is far from green

A failing step halts its scenario, which is right for a gate and wrong for a survey. Pointing the suite at a partial implementation, one divergence in a `create` step hides every divergence behind it, and the target then gets improved one slow rebuild at a time — each round revealing exactly one more thing.

`-keep-going` lets a scenario continue past a step whose **body** diverged from its fixture:

```sh
go run ./conformance/cmd/conformance -endpoint http://localhost:4599 \
    -tier load-bearing -tags subset:floci -keep-going
```

It is a measuring aid, not a softer gate. Only a body mismatch is survivable, because the request still succeeded and the resource still exists, so the captures are good and later steps have something to act on. A wrong status, a wrong error type, or a poll that never settles still halts the scenario — nothing after those can be trusted. Diverging steps are still reported as failures and the exit status is unchanged, so this is safe to leave out of CI and useful to reach for when a second implementation is being brought up.

## Reaching a VM's own endpoint

The runner addresses the control plane and sigv4-signs everything, which left the per-VM endpoint unrecordable and every one of its answers a guess. Two step fields fix that:

```json
{
  "name": "endpoint-valid-token",
  "operation": "VmEndpoint",
  "method": "GET",
  "baseURL": "https://${endpointA}",
  "path": "/",
  "headers": { "X-aws-proxy-auth": "${tokenA}" },
  "expect": { "status": 200 }
}
```

`baseURL` is templated like `path`, so a step can target the hostname captured from an earlier `GetMicrovm`. A step that sets it is **not signed** — the endpoint authenticates with a header credential, and signing it would invent a scheme the service does not use. `headers` is how that credential rides.

That hostname resolves publicly to real AWS, which is right for a recording run and useless against a local target. `-vm-endpoint-rewrite http://127.0.0.1:4566` sends those steps to a given address while leaving the `Host` header intact, the same trick as `curl --resolve`, and needed for the same reason: the target routes on the name.

```sh
go run ./conformance/cmd/conformance -endpoint http://localhost:4566 \
  -vm-endpoint-rewrite http://localhost:4566 -poll-timeout 20
```

A target that does not serve VM endpoints at all can skip the scenario with `-tags`; it carries no `subset:floci` tag, since nothing in CloudFormation reaches an endpoint.

## Rejected fixtures

A fixture renamed to `<step>.json.rejected-<reason>` is a recording the suite refuses to treat as truth, because it captured the recording *account* or *image* rather than the service. They are kept rather than deleted: each one is evidence, and each cost a live session.

`rejected-502` — the probe used the pre-recording guess `mv-…` for a VM id. Real ids are `microvm-<uuid>`, and the API gateway answered the malformed path with an nginx `502` HTML page. Re-recorded clean once the case used a well-formed id.

`rejected-account-history` — `ListMicrovms` returns every VM the account has ever run, terminated ones included and apparently forever. A recorded list is therefore a photograph of one account at one moment and can never equal a fresh target's. Those steps use `itemShape` instead.

`rejected-image-owned-body` — a successful call to a VM's endpoint returns whatever the image serves. The recording captured the `code.zip` app's `{"path":"/","status":"ok","ts":…}`, which says nothing about the service and everything about what happened to be in the bucket. Those steps assert status only, and any target may serve any body — m80 serves its own stub, or whatever `-vm-stub-body` points at.

A fourth category turned out not to exist. Three not-found probes recorded `403 AccessDeniedException` with bodies naming the caller and a capital-M `Message`, and were briefly set aside as an artifact of the recording account's permissions. That diagnosis was wrong: the account holds `AdministratorAccess`, and the real cause was `missingImageArn` carrying the `123456789012` placeholder, which makes the probe *cross-account*. Cross-account access needs a resource-based policy no matter how much admin the caller holds, so the 403 was correct AWS behavior for the ARN actually sent — just not the behavior the case meant to test. `account` is a built-in param now, the same as `region`, and all three re-recorded as the `404 ResourceNotFoundException` their `expect` blocks always claimed.
