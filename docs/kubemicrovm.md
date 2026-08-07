# Standing up KubeMicroVM against m80

KubeMicroVM is a Kubernetes operator for AWS Lambda MicroVMs. Running it normally means an EKS cluster, Pod Identity, and a real AWS account that bills you. This page runs the same operator, unmodified, on a local k3d cluster against m80 instead.

Two things come out of that. You can check a MicroVM deployment reconciles the way you expect before any of it reaches AWS, which is the cheap half of finding out. And m80 gets tested through a real consumer's eyes rather than through its own fixtures.

What it will not tell you is whether your code runs. m80 emulates the control plane, so a MicroVM here is a record with a state machine; nothing fetches your artifact and nothing executes it. See [scope](scope.md).

## What you need first

| Tool | Why |
|---|---|
| docker | Everything runs in it, including the test runner |
| [k3d](https://k3d.io) | Creates the cluster |
| kubectl | The scripts drive the cluster with it |
| helm | cert-manager and the operator chart both install with it |
| node + npm | The cluster's shape is declared (`uat/cluster/cluster.ts`, a self-contained chant mini-project); the harness builds the k3d config from it. m80 itself stays a single Go binary — this is harness infrastructure, not m80's |
| A [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) checkout | Only for running their UAT suite; not needed to bring the stack up |

The AWS CLI is not needed on the host. It ships inside the runner container.

Everything is pulled from public registries, so no AWS account and no credentials are involved at any point.

## Bring it up

```sh
git clone https://github.com/INTENTIUS/m80 && cd m80
just uat-up
```

That takes a few minutes, mostly cert-manager. It ends by printing the operator's connectivity line:

```
stack up. operator health:
  AWS connectivity confirmed: account=000000000000
```

Seeing that line means the operator has started, passed its startup gate, and is talking to the emulated stack. If it does not appear the script exits non-zero and tells you which log to read.

## Check it works

```sh
kubectl apply -f - <<'YAML'
apiVersion: lambda.aws.amazon.com/v1alpha1
kind: MicroVMImage
metadata: { name: hello, namespace: default }
spec:
  region: us-east-1
  baseImageArn: arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1
  buildRoleArn: arn:aws:iam::000000000000:role/demo
  memorySizeMiB: 2048
  source: { s3Bucket: demo, s3Key: code.zip }
YAML

kubectl get microvmimage hello -o jsonpath='{.status.activeVersion}'   # 1.0
```

The bucket and key need not exist, because m80 records the reference without fetching it. Then run one:

```sh
kubectl apply -f - <<'YAML'
apiVersion: lambda.aws.amazon.com/v1alpha1
kind: MicroVM
metadata: { name: hello-vm, namespace: default }
spec:
  region: us-east-1
  imageRef: hello
  desiredState: Running
  autoResumeEnabled: true
  maxIdleDurationSeconds: 900
YAML

kubectl get microvm hello-vm -o jsonpath='{.status.state}'      # Running
kubectl patch microvm hello-vm --type merge -p '{"spec":{"desiredState":"Suspended"}}'
```

To watch it from m80's side, `kubectl -n kube-microvm port-forward svc/m80 4291:4290` and point the AWS CLI at `http://localhost:4291`.

Note `source.s3Bucket` and `source.s3Key` as flat fields; a nested `source.s3.bucket` is rejected by the CRD's strict decoding.

From here `kubectl` works normally against the cluster. A MicroVM CR applied to the `default` namespace is reconciled by the operator, which calls m80, and `kubectl get microvm` shows it running.

## Run their UAT suite

```sh
KUBEMICROVM=/path/to/KubeMicroVM just uat-run
just uat-down
```

Results land in `uat-results/`, of which `report.html` is the readable one. `uat/matrix.py` turns `output.xml` into the table below.

## What is running

| Piece | Why |
|---|---|
| k3d cluster | Stands in for EKS |
| m80 | Serves the MicroVMs API, and `sts:GetCallerIdentity` for the operator's startup gate |
| cert-manager | The operator's admission webhooks need it |
| KubeMicroVM operator | Installed from GHCR, unmodified |
| Runner container | Robot Framework, `kubectl`, the `microvm` CLI, the AWS CLI |

Two containers, m80 and the operator, plus cert-manager and k3d's own. m80 is 8 MiB.

An earlier version of this harness also ran floci, a full AWS emulator, purely so something would answer the one `sts:GetCallerIdentity` call the operator's startup gate makes. That cost 556 MiB of image and 190 MiB of memory to return a 400-byte XML document, so m80 answers it directly under `-serve-sts`. It is a shim rather than an emulation: every other STS action gets a 501 saying so.

Worth stating because it reads the other way round at first glance: **the MicroVMs module proposed for floci is not involved here and is not needed.** That module is a second implementation of the same API m80 implements, for provisioning `AWS::Lambda::MicrovmImage` through CloudFormation, and the operator never takes the CloudFormation path.

Nor does this harness need the S3 bucket or the IAM build role to exist. m80 accepts both as opaque strings and fetches nothing, so every case here passes against a bucket that was never created. Validating the provisioning of those prerequisites is a real and separate job, and [docs/floci.md](floci.md) says which layer does it.

Every default is an environment variable — `NS`, `M80_IMAGE`, `CHART_VERSION`, `REGION`, `MAX_ACCOUNT_MEMORY_MIB` — except the cluster itself, whose name and shape are declared in `uat/cluster/cluster.ts` and built into the config `k3d cluster create --config` consumes. Read the top of `uat/up.sh` for the current values.

To test a build of your own, `M80_IMAGE=m80:candidate ./uat/up.sh` — a locally built image is imported into the cluster rather than pulled.

## Deviations from the upstream UAT, and why

Each of these is a difference between this harness and the EKS run the suite was written for. They are worth reading before trusting a result, and several are worth fixing upstream.

**No Pod Identity.** Static `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` of `test`/`test` go on the operator instead. m80 reads the region out of the sigv4 credential scope and validates no signature, so the values are irrelevant as long as they exist. `AWS_EC2_METADATA_DISABLED=true` stops the SDK stalling on IMDS that is not there.

**The operator's startup gate calls real STS.** `AwsConnectivityStartup` runs `sts:GetCallerIdentity` at boot with a region and no endpoint override, so `AWS_MICROVM_ENDPOINT` does not reach it. Without a reachable STS the health check reports `awsConnectivity: false` forever, readiness never passes, the webhook service gets no endpoints, and every CR create fails with `no endpoints available`. Pointing `AWS_ENDPOINT_URL_STS` at m80, which answers that one action under `-serve-sts`, fixes it. Filed upstream as [KubeMicroVM#50](https://github.com/codriverlabs/KubeMicroVM/issues/50), where the maintainers have said they intend to add an override; when it lands the shim can go.

**The chart only templates the env keys it knows.** `--set app.envs.AWS_ACCESS_KEY_ID=…` is silently dropped, so credentials and the STS override are patched in with `kubectl set env` after install. Filed as [KubeMicroVM#52](https://github.com/codriverlabs/KubeMicroVM/issues/52).

**cert-manager is a prerequisite** and nothing in the UAT README says so. The operator chart declares `Certificate` and `Issuer` resources, and the install fails outright without the CRDs. Also [#52](https://github.com/codriverlabs/KubeMicroVM/issues/52).

**The namespace label is `lambda.aws.amazon.com/manage-microvms=true`.** Undocumented, and the admission webhook rejects CRs without it.

**A Service named `floci` breaks floci.** Kubernetes injects `FLOCI_PORT=tcp://10.43.x.x:4566` into pods in the namespace, Quarkus maps that onto the `floci.port` config property, and startup fails parsing it as an integer. `enableServiceLinks: false` on that pod.

**The suite runs in a container, not on the host.** The `microvm` CLI is released for `linux/amd64` and `linux/arm64` only, so a macOS host cannot run the CLI-dependent cases at all. The runner container joins k3d's docker network and reaches the API server at its internal address.

**The runner cannot resolve cluster DNS.** It is a container on that network rather than a pod, so `*.svc.cluster.local` does not resolve for it. Anything the suite drives with the AWS CLI, which is how the drift cases terminate a MicroVM out of band, needs m80 at a docker-network address. That is what the `m80-node` NodePort and `AWS_ENDPOINT_URL` are for. Without them the CLI builds a correct URL and cannot connect, and the drift cases fail as "no drift detected" with nothing pointing at the cause.

**The account memory ceiling is raised.** m80 defaults to the 4096 MiB ceiling recorded from a fresh AWS account, which at the 2048 MiB default tier is two concurrent MicroVMs. The UAT runs more than two at once, and a real account used for UAT would have had its quota raised, so the harness raises it too.

**Do not restart m80 under a live operator.** m80 holds everything in memory, so a restart makes every MicroVM it knew about return `404`. The operator then retries cleanup on the orphaned CRs every ten seconds forever and never removes its finalizer, leaving objects that cannot be deleted without `kubectl patch`. Tear the cluster down and bring it back up instead.

**S3 sources are never fetched.** The image fixtures name a bucket and key; m80 records the reference without fetching, so they need not exist. `ACCOUNT_ID` is `000000000000`, m80's account, so ARNs match what the operator reads back.

## Pass matrix

<!-- matrix:start -->
First recorded 2026-08-01 against m80 v0.1.0 and operator 1.0.11; current record 2026-08-06 against operator chart 1.0.12, whose finalizer fix ([KubeMicroVM#51](https://github.com/codriverlabs/KubeMicroVM/issues/51) — a bug this harness found) moved two cases into the passing column. Since #61 landed this is no longer a snapshot: CI runs the whole suite against every commit's build (`.github/workflows/uat.yml`) and holds the run to exactly this matrix in both directions — a new failure fails the build, and a listed failure that starts passing fails it too, until this page and `uat/expected-failures.txt` move with it.

**52 of 63 cases pass.**

| | Suite | Passed |
|---|---|---|
| ⚠️ | 00 Cluster Setup | 6/7 |
| ⚠️ | 01 Quick Start | 8/9 |
| ⚠️ | 02 Rbac | 6/8 |
| ⚠️ | 03 Networking | 2/5 |
| ⚠️ | 04 Pod Token Injection | 8/9 |
| ✅ | 05 Replicaset | 6/6 |
| ✅ | 06 Microvm Class | 6/6 |
| ⚠️ | 07 Drift Autosuspend | 4/5 |
| ⚠️ | 08 Memory Sizing | 5/6 |
| ⚠️ | 99 Final Cleanup | 1/2 |

### Why the eleven fail

None is m80 answering differently from real AWS.

**Reach the endpoint but not m80 (3).** `NET-02`, `INJ-08`, `AUTO-02`, all `Token authentication failed`. The token is minted by m80 correctly; the suite then curls `https://<uuid>.lambda-microvm.<region>.on.aws/`, that hostname resolves to real AWS, and AWS rejects an m80-issued token. The call never reaches m80. Recording the endpoint against live AWS in [#42](https://github.com/INTENTIUS/m80/issues/42) confirmed this from the other end: `Token authentication failed` is verbatim what AWS returns for a token that does not match the VM the hostname names. Reaching m80 instead needs wildcard DNS *and* TLS, tracked in [#45](https://github.com/INTENTIUS/m80/issues/45).

**Lose a race with the operator's resync (5).** `QS-07`, `RBAC-05`, `NET-01`, `NET-04`, `MEM-07`, reporting `Endpoint for <vm> did not resolve within 60s`. Not DNS: the UAT polls the CR field `.status.endpointUrl`, which the operator leaves at `PENDING` until its next reconcile. Measured at 61 to 65 seconds against a 60 second allowance. Raising `-build-delay` tenfold moved it four seconds, so the interval is the operator's, and the race is as tight against real AWS. A case on the losing side of this race sometimes gets far enough to fail as the first group instead — the two groups trade members between runs; their union is stable.

**Want an AWS-side identity decision (2).** `Pod Identity Association Exists` has none on k3d. `RBAC-06` expects `not authorized`; m80 accepts and echoes IAM without evaluating it, which [scope.md](scope.md) refuses on purpose.

**Inherit the debris (1).** `99 Final Cleanup` counts every resource left behind, and the endpoint-race failures above it abandon theirs — `MicroVMImages still exist` names whichever image `MEM-07` was mid-verifying when its clock ran out. It fails as a consequence, not a cause; it would pass the moment the race group did.

Until chart 1.0.12, a fifth group existed: `QS-08` and `RS-06` (and `99` for this reason rather than debris) hit an operator bug m80's fidelity exposed — a deleted CR whose finalizer never cleared. That is [#51](https://github.com/codriverlabs/KubeMicroVM/issues/51), fixed upstream in v1.0.12, and verifying the fix took one more recording: the operator's new cleanup re-issues the terminate, m80 was answering the retry with an extrapolated 400, and the real service turned out to accept it idempotently ([#83](https://github.com/INTENTIUS/m80/issues/83), recorded 2026-08-06). The emulator was the last thing standing between the fix and the passing column — which is the correct order for an emulator to be wrong in: visibly, and once.

<!-- matrix:end -->

## Reading a failure

Two kinds, and they are not the same thing.

An m80 fidelity gap means m80 answered in a way real AWS would not. Those become issues against m80.

A UAT assumption about real AWS means the case needs something no emulator can provide, or reaches past the endpoint override. Those are noted here rather than filed, because m80 cannot fix them.

`microvm --direct` is the clearest example of the second kind. The flag exists to bypass the operator and build its own SDK client, and it honours neither `AWS_MICROVM_ENDPOINT` nor `AWS_ENDPOINT_URL`, so it always talks to real AWS. Against real credentials it works; against `test`/`test` it returns `403 The security token included in the request is invalid` with a genuine AWS request id. Any case relying on `--direct` is untestable against any emulator.
