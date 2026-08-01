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

**S3 sources are never fetched.** The image fixtures name an S3 bucket and key; m80 records the reference without fetching, so the fixtures need not exist. `ACCOUNT_ID` is `000000000000`, m80's account, so ARNs match what the operator reads back.

## Pass matrix

<!-- matrix:start -->
Run 2026-08-01 against m80 v0.1.0, operator 1.0.11, excluding the performance suite.

**47 of 63 cases pass.**

| | Suite | Passed |
|---|---|---|
| ⚠️ | 00 Cluster Setup | 6/7 |
| ⚠️ | 01 Quick Start | 6/9 |
| ⚠️ | 02 Rbac | 6/8 |
| ⚠️ | 03 Networking | 2/5 |
| ⚠️ | 04 Pod Token Injection | 8/9 |
| ⚠️ | 05 Replicaset | 5/6 |
| ✅ | 06 Microvm Class | 6/6 |
| ⚠️ | 07 Drift Autosuspend | 2/5 |
| ⚠️ | 08 Memory Sizing | 5/6 |
| ⚠️ | 99 Final Cleanup | 1/2 |

### Why the sixteen fail

Not one is a case where m80 answered differently from real AWS. They fall into four groups.

**Reaches past the endpoint override to real AWS (4).** `QS-06`, `QS-07`, `NET-02`, `AUTO-02`. All go through `microvm --direct`, a debug flag that deliberately builds its own SDK client and honours neither `AWS_MICROVM_ENDPOINT` nor `AWS_ENDPOINT_URL`. The giveaway is the error: `403 The security token included in the request is invalid` carrying a genuine AWS request id. Untestable against any emulator, not just m80.

**Needs the VM endpoint to resolve (4).** `RBAC-05`, `NET-01`, `NET-04`, `MEM-07`. The hostname m80 hands out resolves to real AWS from inside the cluster, so the curl never arrives. Tracked as [#45](https://github.com/INTENTIUS/m80/issues/45) — a DNS shim in this harness is probably the fix.

**Assumes AWS-side identity (3).** `Pod Identity Association Exists` — there is no Pod Identity on k3d. `RBAC-06` and `INJ-08` expect an authorization decision; m80 accepts and echoes IAM without ever evaluating it, which `docs/scope.md` refuses on purpose.

**Teardown and drift (5).** `QS-08`, `RS-06`, `DRIFT-01`, `DRIFT-02`, and the final cleanup check. A `rs-pool-…` MicroVM outlives its ReplicaSet, and the drift cases need a VM terminated behind the operator's back — m80 has that lever as a Go API but not over HTTP, so a suite running against the container cannot reach it. These are the group worth digging into next; they are the only ones that might yet turn up a genuine fidelity gap.

<!-- matrix:end -->

## Reading a failure

Two kinds, and they are not the same thing:

- **An m80 fidelity gap** — m80 answered in a way real AWS would not. These become issues against m80.
- **A UAT assumption about real AWS** — the case needs something no emulator can provide, or reaches past the endpoint override. These are noted here rather than filed, because m80 cannot fix them.

`microvm --direct` is the clearest example of the second kind. The flag exists to bypass the operator and build its own SDK client, and it honours neither `AWS_MICROVM_ENDPOINT` nor `AWS_ENDPOINT_URL`, so it always talks to real AWS. Against real credentials it works; against `test`/`test` it returns `403 The security token included in the request is invalid` with a genuine AWS request id. Any case relying on `--direct` is untestable against any emulator.
