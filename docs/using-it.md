# Using it

The operator is the reason m80 exists, but it is a standalone target for anything speaking the MicroVMs API. The M-80 is the most famous firecracker there is, and m80 emulates a Firecracker-backed service.

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80

curl -s localhost:4290/_m80/health | jq '.coverage.implemented, .coverage.total'
```

Point any SDK at it with an endpoint override — m80 reads the region out of the sigv4 credential scope, so one instance serves every region and the credentials need not be real:

```sh
aws configure set aws_access_key_id test
aws configure set aws_secret_access_key test

aws --endpoint-url http://localhost:4290 --region us-east-2 \
    lambda-microvms list-microvm-images
```

```go
cfg, _ := config.LoadDefaultConfig(ctx,
    config.WithRegion("us-east-2"),
    config.WithBaseEndpoint("http://localhost:4290"))
```

## Every flag

Nothing here is required — m80 with no arguments is the one in the quick start.

| Flag | Default | What it does |
|------|---------|--------------|
| `-addr` | `:4290` | Listen address |
| `-log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `-version` | | Print the version and exit |
| `-build-delay` | `1s` | How long one build state transition takes on the injected clock |
| `-vm-stub-body` | | File whose contents a VM's endpoint returns, instead of m80's default |
| `-serve-sts` | off | Answer `sts:GetCallerIdentity`, for a consumer whose startup gate calls it. Not an STS emulation — every other action gets 501 |
| `-enable-injection` | off | Expose the failure-injection levers at `POST /_m80/inject` |
| `-max-account-memory-mib` | `4096` | Account allocated-memory ceiling across running VMs; `0` to uncap |
| `-max-microvms` | uncapped | Cap on non-terminal VMs |
| `-max-concurrent-snapshot-creates` | uncapped | Cap on in-flight image builds |
| `-throttle-requests-per-interval` | off | Requests allowed per interval before throttling |
| `-throttle-interval-seconds` | `1` | Length of the throttle interval |
| `-throttle-reason` | | `ThrottleReason` on throttles for the connector and tags families |
| `-throttle-retry-after-seconds` | | `Retry-After` on throttles; `0` omits it |

`scripts/docs-consistency.sh` fails the build when this table and the binary disagree in either direction, so a flag added without a row stops CI.

## The whole loop, from nothing to a running VM

Every operation the AWS CLI knows works against m80. The one thing worth saying up front is that the identifier flags are not uniform — images take `--image-identifier`, VMs take `--microvm-identifier` — which is the service's naming, not m80's.

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_ENDPOINT_URL=http://localhost:4290 AWS_REGION=us-east-2
alias mv='aws lambda-microvms'

# Build an image. Nothing is fetched, so the S3 uri and the role need not exist.
mv create-microvm-image --name demo \
    --base-image-arn arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1 \
    --build-role-arn arn:aws:iam::000000000000:role/demo \
    --code-artifact uri=s3://demo/code.zip

mv get-microvm-image-version --image-identifier demo --image-version 1.0 \
    --query state          # CREATING, then SUCCESSFUL after -build-delay

# Run one, and wait for it.
VM=$(mv run-microvm --image-identifier demo --query microvmId --output text)
mv get-microvm --microvm-identifier "$VM" --query '[state,endpoint]'

# Suspend it, resume it, terminate it.
mv suspend-microvm --microvm-identifier "$VM"
mv resume-microvm  --microvm-identifier "$VM"
mv terminate-microvm --microvm-identifier "$VM"
```

Terminated VMs stay listed, and an image cannot be deleted while a live VM references it. Both are recorded behaviour, and both surprise people.

Everything is in memory and nothing is written, so a restart is a clean account. m80 is stateful within a run — image names stay reserved through the async delete window, exactly as the real service does — so a suite that runs twice against one instance will fail the second time on `already exists`. That is fidelity, not a bug; restart between runs.

## Making a build take as long as you want

Transitions run on an injected clock, so `-build-delay` decides how long a build takes to become runnable. Short for tests, longer for a demo where the intermediate states should be visible:

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80 -build-delay 300ms
```

## Making things fail on purpose

Failure paths are what a consumer most needs a test target for, and they are the ones real AWS will not produce on request. `-enable-injection` exposes the levers to any client:

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80 -enable-injection

# the next build of this image settles FAILED
curl -X POST localhost:4290/_m80/inject -d '{"target":"build","name":"doomed"}'

# the next connector of this name settles FAILED with a real reason code
curl -X POST localhost:4290/_m80/inject \
     -d '{"target":"connector","name":"egress","reasonCode":"SubnetOutOfIPAddresses"}'
```

Off by default: nothing under `/_m80/` is signed, so the flag is the consent. The response carries `"injected": true`, which a state m80 reached on its own never does — so a test cannot mistake an injected failure for a real one. See [lifecycle](lifecycle.md#drift-levers).

## Calling a VM's endpoint

Each VM gets an endpoint hostname, and m80 answers it from the same process. Issue a token, then present it as `X-aws-proxy-auth`:

```sh
TOK=$(curl -s -X POST "$M80/2025-09-09/microvms/$VM/auth-token" \
  -d '{"expirationInMinutes":60,"allowedPorts":[{"allPorts":{}}]}' \
  | jq -r '.authToken["X-aws-proxy-auth"]')

# by the hostname the control plane handed out
curl "$M80/" -H "Host: $(curl -s "$M80/2025-09-09/microvms/$VM" | jq -r .endpoint)" \
  -H "X-aws-proxy-auth: $TOK"

# or by path, when forging a Host header is inconvenient
curl "$M80/_m80/vm/$VM/" -H "X-aws-proxy-auth: $TOK"
```

The response body is m80's default stub, or whatever `-vm-stub-body <file>` points at — against real AWS this is the image's own app, so it is the one part of the endpoint m80 cannot copy. Either way an `X-M80-State-Marker` header carries a counter that survives suspend and resume, so a client can prove the VM kept its state.

Everything else about the endpoint is recorded rather than guessed, as of 2026-08-01:

| Situation | Answer |
|---|---|
| No token, or one that cannot be parsed | `403 Request missing authentication` |
| A token for another VM, or a hostname naming none | `403 Token authentication failed` |
| A token that does not grant the port | `200` — `allowedPorts` does not gate this endpoint |
| Suspended, `autoResumeEnabled` set | `200`, and the VM is `RUNNING` afterwards |
| Suspended without it, or terminated | `502` with an empty body |

## Throttles and quotas

The account memory ceiling is recorded live and on by default: six concurrent `RunMicrovm` calls on a fresh account admit two 2048 MiB VMs and reject four with `402 ServiceQuotaExceededException`. Throttling was never observed against the real service — the memory ceiling fires first and hides it — so it is off unless asked for.

```sh
m80 -max-account-memory-mib 8192          # raise the recorded 4096 ceiling
m80 -max-microvms 3                       # cap by count instead
m80 -max-concurrent-snapshot-creates 1    # cap in-flight image builds
m80 -throttle-requests-per-interval 10 -throttle-interval-seconds 1 \
    -throttle-reason CallerRateLimitExceeded -throttle-retry-after-seconds 5
```

`-throttle-reason` is observable on the connector and tags operations, whose models carry `TooManyRequestsException.Reason`. The MicroVM family has no such member and throttles as `ThrottlingException` instead.

**The memory ceiling is the one that catches people.** 4096 MiB is the number recorded from a fresh AWS account, and at the 2048 MiB default tier it is two concurrent MicroVMs — a third gets `402 ServiceQuotaExceededException`. Since that recording, the ceiling has become visible directly: it is the Service Quotas entry "Max allocated MicroVM memory" (lambda, `L-CD1C0CC4`), and a fresh account read 8 GB there on 2026-08-06 — so the base varies per account, and yours is one CLI call away rather than an inference. The recorded 402 body names no number and no knob, so m80 logs one:

```
WARN run rejected: account memory ceiling reached allocatedMiB=4096 requestedMiB=2048
     ceilingMiB=4096 hint="raise -max-account-memory-mib, or 0 to uncap"
```

Anything running more than two VMs at once — a ReplicaSet, an operator test suite — wants `-max-account-memory-mib` raised. Pointing KubeMicroVM's UAT at m80 lost twenty of twenty-eight cases to this before the ceiling was raised, and the operator reported it as a timeout.

## Pointing KubeMicroVM at m80

The operator supports endpoint override out of the box (#17). All three of its SDK clients honor one env var on the operator deployment:

```
AWS_MICROVM_ENDPOINT=http://m80:4290
```

The `microvm` CLI's default token path goes through the operator's sub-resource and needs nothing. Only the CLI's `--direct` debug flag builds its own SDK client without an override.

One more flag matters for that path and is easy to miss: the operator's startup gate calls `sts:GetCallerIdentity` before it will report ready, with no endpoint override of its own, so pointing `AWS_ENDPOINT_URL_STS` at an m80 started with `-serve-sts` is what lets it boot at all. It is a shim for that one action, not an STS emulation — everything else under STS answers 501. Filed upstream as [KubeMicroVM#50](https://github.com/codriverlabs/KubeMicroVM/issues/50); when the override lands the shim can go.

[Standing up KubeMicroVM](kubemicrovm.md) is the full walkthrough, from an empty machine to a reconciling operator.

## What it is not

Not a CloudFormation emulator. If you deploy MicroVMs through CFN or CDK rather than through the operator, that path is served by a [floci](https://github.com/floci-io/floci) module, since CFN emulation can only live where the CFN engine lives. The two cover different deployment paths and share one conformance suite; see [floci](floci.md).

Not the operator. Replica pools, classes, and token sidecars are KubeMicroVM constructs rather than service API, so m80 models what the operator calls, not what it does.

Not a way to run your code. It is the limit worth repeating: nothing fetches your artifact and nothing runs it.
