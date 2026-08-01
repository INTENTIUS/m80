# KubeMicroVM UAT against m80

KubeMicroVM ships a Robot Framework UAT suite that normally needs a live EKS cluster, Pod Identity, and a real AWS account. This harness runs it on k3d against m80 instead, which is the point of m80 existing: it tests through a real consumer's eyes rather than through our own fixtures.

```sh
just uat-up                                   # k3d + m80 + floci + operator
KUBEMICROVM=/path/to/KubeMicroVM just uat-run  # the suite
just uat-down
```

Results land in `uat-results/` — `report.html` is the readable one.

## What the stack looks like

| Piece | Why |
|---|---|
| k3d cluster | Stands in for EKS |
| **m80** | Serves the MicroVMs API, via the operator's `AWS_MICROVM_ENDPOINT` |
| **floci** | Serves **STS only** — see the connectivity gate below |
| cert-manager | The operator's admission webhooks need it |
| KubeMicroVM operator | Installed from GHCR, unmodified |
| Runner container | Robot + `kubectl` + `microvm` CLI + `aws` CLI |

m80 and floci are not alternatives here; they compose. m80 does not emulate STS and is not going to — it models one service.

## Deviations from the upstream UAT, and why

Every one of these is a difference between this harness and the EKS run the suite was written for.

**No Pod Identity.** Static `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` of `test`/`test` are set on the operator instead. m80 reads the region out of the sigv4 credential scope and validates no signature, so the values are irrelevant as long as they exist. `AWS_EC2_METADATA_DISABLED=true` stops the SDK stalling on IMDS that isn't there.

**The operator's startup gate calls real STS.** `AwsConnectivityStartup` runs `sts.getCallerIdentity()` at boot and builds the client with a region and *no endpoint override*, so `AWS_MICROVM_ENDPOINT` does not reach it. Without a reachable STS the health check reports `awsConnectivity: false` forever, readiness never passes, the webhook service gets no endpoints, and every CR create fails with `no endpoints available`. Pointing `AWS_ENDPOINT_URL_STS` at floci fixes it — the operator then logs `AWS connectivity confirmed: account=000000000000`. **This is worth an upstream issue**: as it stands the operator cannot come up against any emulated endpoint, which is a wider problem than m80.

**The chart only templates the env keys it knows.** `--set app.envs.AWS_ACCESS_KEY_ID=...` is silently dropped, so credentials and the STS override are patched in with `kubectl set env` after install. Worth an upstream issue too, smaller.

**cert-manager is a prerequisite** and nothing in the UAT README says so. The operator chart declares `Certificate` and `Issuer` resources, and the install fails outright without the CRDs.

**The namespace label is `lambda.aws.amazon.com/manage-microvms=true`.** Not documented in the UAT README; the admission webhook rejects CRs without it.

**A Service named `floci` breaks floci.** Kubernetes injects `FLOCI_PORT=tcp://10.43.x.x:4566` into pods in the namespace, Quarkus maps that onto the `floci.port` config property, and startup fails parsing it as an integer. `enableServiceLinks: false` on that pod.

**The suite runs in a container, not on the host.** The `microvm` CLI is released for `linux/amd64` and `linux/arm64` only, so a macOS host cannot run the CLI-dependent cases at all. The runner container joins k3d's docker network and reaches the API server at its internal address.

**Do not restart m80 under a live operator.** m80 holds everything in memory, so a restart makes every MicroVM it knew about return `404`. The operator then retries cleanup on the orphaned CRs every ten seconds forever and never removes its finalizer, leaving objects that cannot be deleted without `kubectl patch`. Tear the cluster down and bring it back up instead. Filed upstream-side in [#47](https://github.com/INTENTIUS/m80/issues/47).

**The runner cannot resolve cluster DNS.** It is a container on k3d's docker network, not a pod, so `*.svc.cluster.local` does not resolve for it. Anything the suite drives with the AWS CLI — the drift cases terminate a MicroVM out of band that way — needs m80 reachable at a docker-network address, which is what the `m80-node` NodePort and `AWS_ENDPOINT_URL` are for. Without them the CLI builds a correct URL and simply cannot connect, and the drift cases fail as "no drift detected" with nothing pointing at the cause.

**S3 sources are never fetched.** The image fixtures name an S3 bucket and key; m80 records the reference without fetching, so the fixtures need not exist. `ACCOUNT_ID` is `000000000000`, m80's account, so ARNs match what the operator reads back.

## Pass matrix

<!-- matrix:start -->
Run 2026-08-01 against m80 v0.1.0 and operator 1.0.11, excluding the performance suite.

**50 of 63 cases pass.**

| | Suite | Passed |
|---|---|---|
| ⚠️ | 00 Cluster Setup | 6/7 |
| ⚠️ | 01 Quick Start | 7/9 |
| ⚠️ | 02 Rbac | 6/8 |
| ⚠️ | 03 Networking | 2/5 |
| ⚠️ | 04 Pod Token Injection | 8/9 |
| ⚠️ | 05 Replicaset | 5/6 |
| ✅ | 06 Microvm Class | 6/6 |
| ⚠️ | 07 Drift Autosuspend | 4/5 |
| ⚠️ | 08 Memory Sizing | 5/6 |
| ⚠️ | 99 Final Cleanup | 1/2 |

### Why the thirteen fail

None is m80 answering differently from real AWS. In two cases m80 answering *exactly* as real AWS is what exposes an operator bug.

**Reach the endpoint but not m80 (4).** `QS-07`, `NET-02`, `INJ-08`, `AUTO-02` — all `Token authentication failed`. The token is minted by m80 correctly; the suite then curls `https://<uuid>.lambda-microvm.<region>.on.aws/`, that hostname resolves to *real AWS*, and AWS rejects an m80-issued token. The call never reaches m80. [#45](https://github.com/INTENTIUS/m80/issues/45) — and it needs wildcard DNS *and* TLS, not just DNS.

**Lose a race with the operator's resync (4).** `RBAC-05`, `NET-01`, `NET-04`, `MEM-07`, reporting `Endpoint for <vm> did not resolve within 60s`. Not DNS: the UAT polls the CR field `.status.endpointUrl`, which the operator leaves at `PENDING` until its next reconcile. Measured at 61–65s against a 60s allowance. Raising `-build-delay` tenfold moved it four seconds, so the interval is the operator's, and the race is as tight against real AWS.

**Want an AWS-side identity decision (2).** `Pod Identity Association Exists` — none on k3d. `RBAC-06` expects `not authorized`; m80 accepts and echoes IAM without evaluating it, which `docs/scope.md` refuses on purpose.

**Hit an operator bug that m80's fidelity exposes (3).** `RS-06`, `99 Final Cleanup`, and `QS-08`. A deleted MicroVM CR keeps its finalizer forever while the operator logs `Cleaning up …` every ten seconds. The MicroVM is present and correctly `TERMINATED`, with `stateReason: "Success."`. The cleanup path appears to wait for it to *disappear* — and it never does, because **terminated MicroVMs stay listed**, which is recorded live behaviour. Against real AWS the same CR would hang for as long as AWS retains the VM. Reported in [#47](https://github.com/INTENTIUS/m80/issues/47).

<!-- matrix:end -->

## Reading a failure

Two kinds, and they are not the same thing:

- **An m80 fidelity gap** — m80 answered in a way real AWS would not. These become issues against m80.
- **A UAT assumption about real AWS** — the case needs something no emulator can provide, or reaches past the endpoint override. These are noted here rather than filed, because m80 cannot fix them.

`microvm --direct` is the clearest example of the second kind. The flag exists to bypass the operator and build its own SDK client, and it honours neither `AWS_MICROVM_ENDPOINT` nor `AWS_ENDPOINT_URL`, so it always talks to real AWS. Against real credentials it works; against `test`/`test` it returns `403 The security token included in the request is invalid` with a genuine AWS request id. Any case relying on `--direct` is untestable against any emulator.
