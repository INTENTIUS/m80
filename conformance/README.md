# Conformance suite

One suite, three targets ([docs/conformance.md](../docs/conformance.md)). Cases and fixtures are JSON; the runner is Go, for sigv4.

```sh
# Against an m80 instance (dummy credentials are automatic)
go run ./conformance/cmd/conformance -endpoint http://localhost:4290

# The floci CFN subset only
go run ./conformance/cmd/conformance -endpoint http://localhost:4566 -tags subset:floci

# Recording fixtures against real AWS (env credentials, real params)
AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… \
go run ./conformance/cmd/conformance -endpoint https://lambda.us-east-1.amazonaws.com -record \
  -param buildRoleArn=arn:aws:iam::<account>:role/<build-role> \
  -param codeArtifactUri=s3://<bucket>/<key>
```

Scenario files live in `cases/`, ordered by filename. Each is self-contained: it creates what it needs and cleans up. `params` in a scenario are template defaults so emulator runs need no flags; `-param` overrides them for recording. Steps poll with `until` where a state must settle, with timeouts sized for real AWS (emulators settle in milliseconds, the poll exits on first match).

Outcomes: `pass`, `fail`, `unimplemented` (HTTP 501, valid for a target under construction), `skipped` (an earlier step in the scenario did not pass), `error`. The report ends with coverage against `inventory.json`, the 29-operation list generated from the verified service models. A fixture in `fixtures/<scenario>/<step>.json` upgrades a case from documented-only to fixture-backed: the response must then equal the fixture after normalization (accounts, ARNs, timestamps, UUIDs, and token-named fields are redacted on both sides).
