# m80

**Check a KubeMicroVM deployment before it costs you anything.**

Running the [KubeMicroVM](https://github.com/codriverlabs/KubeMicroVM) operator normally means an EKS cluster and an AWS account that bills you. m80 is a local emulator of the AWS Lambda MicroVMs API, so the same operator, unmodified, runs on k3d against your laptop.

```sh
docker run --rm -p 4290:4290 ghcr.io/intentius/m80
```

One container, 8 MiB, no companion process. Point any AWS SDK at it with an endpoint override; the credentials need not be real.

It emulates the **control plane**: a MicroVM here is a record with a state machine and a clock. Configuration, reconciliation, lifecycle, RBAC, quotas and teardown are real; nothing fetches your artifact and nothing runs it.

## Give this to your agent

```
There is a local emulator of the AWS Lambda MicroVMs API at http://localhost:4290
(start it with: docker run --rm -p 4290:4290 ghcr.io/intentius/m80).

Point any AWS SDK or the AWS CLI at it with an endpoint override. Credentials
need not be real — it reads the region out of the sigv4 credential scope, so one
instance serves every region.

  aws --endpoint-url http://localhost:4290 --region us-east-2 lambda-microvms list-microvm-images

It emulates the control plane only: nothing fetches your artifact and nothing
runs it. Before assuming a behaviour, read these two pages —
  https://intentius.github.io/m80/scope/       what it emulates and what it refuses
  https://intentius.github.io/m80/unrecorded/  the seven places it answers something
                                               it never observed, and the rule for it

Two things surprise people: terminated VMs stay listed, and an image cannot be
deleted while a live VM references it. Both are recorded behaviour, not bugs.
```

## Start here

[**Using it**](using-it.md) is the whole loop, from nothing to a running VM. [**Standing up KubeMicroVM**](kubemicrovm.md) is the operator on k3d against m80, which is why m80 exists.

Then, in whatever order the question arrives in: [scope](scope.md) is what it emulates and what it refuses, [when m80 has not seen it](unrecorded.md) is where it is guessing and the rule it follows, [API surface](api-surface.md) is the 29 operations and their sources of truth, [lifecycle](lifecycle.md) is the state machines and the failure-injection levers, [conformance](conformance.md) is the suite that gates it, and [floci](floci.md) is why CloudFormation emulation lives elsewhere. What is planned or open lives in [the issues](https://github.com/INTENTIUS/m80/issues), nowhere else.

## Status

Released. All 29 operations answer, `/_m80/health` reports 29/29 with nothing pending, and the conformance suite runs 100 checks against fixtures recorded from live AWS. [KubeMicroVM's own UAT suite](kubemicrovm.md) passes 50 of its 63 cases, with every failure accounted for — three of them bugs in the operator, filed upstream and acknowledged.

Nothing else like it exists. Verified 2026-07-29 across moto, LocalStack (archived), fakecloud, ministack, floci upstream and forks, and every public repo that codes against the MicroVM API. The service went GA 2026-06-22.
