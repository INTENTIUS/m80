# Division of labor with floci

## The split

| Concern | Home | Why |
|---------|------|-----|
| Full-fidelity service emulation | m80 | Owned cadence against a churning preview-fresh API, small container the KubeMicroVM community can adopt, mudflaps mold |
| `AWS::Lambda::MicrovmImage` and `AWS::Lambda::NetworkConnector` through CloudFormation | floci | CFN emulation can only live where the CFN engine lives. chant's `MicrovmApp` and the kit's image stack deploy through CFN |
| Conformance contract | shared suite | See [conformance.md](conformance.md) |

They are not alternatives. The [KubeMicroVM harness](kubemicrovm.md) runs both at once, m80 for the MicroVMs API and floci for STS, because m80 models one service and is not going to grow a second.

## Asymmetric scope

The floci module implements the subset CFN provisioning needs: image create, get and delete, build lifecycle enough for stack create and delete to converge, and network connectors, since `MicrovmApp` emits `AWS::Lambda::NetworkConnector` when VPC egress is requested. It does not need tokens, endpoint stubs, idle timers, or drift levers.

Scoping it narrow keeps the second implementation cheap and the drift surface small. m80 aims at both tiers because exactness is nearly free for a purpose-built emulator; the module is gated on the load-bearing set only.

The [`subset:floci`](https://github.com/INTENTIUS/m80/blob/main/conformance/SUBSET-FLOCI.md) tag is that gate: three scenarios, 26 steps, each one existing because a CloudFormation behaviour depends on it. It is written to be run from somewhere else and points at any endpoint claiming to serve the API.

## Why sparse response bodies are the thing that matters

The KubeMicroVM operator runs its own drift detection, which works by reading back what it wrote and comparing. A service that does not echo `codeArtifact`, `buildRoleArn`, `baseImageArn`, `description` and `tags` looks permanently drifted: the reconciler writes, re-reads, sees a difference, and writes again. That is a reconcile loop that never converges, and it is the first thing anyone pointing an operator at a partial implementation would hit.

`latestActiveImageVersion` set on a still-`CREATING` image is the same class of problem, since a reconciler reads it and launches a VM off a version that does not exist yet.

This is why the subset checks response membership rather than just status codes.

## How floci is built, for anyone contributing to the module

Floci is Quarkus. Services are JAX-RS controllers registered in `ResolvedServiceCatalog` as descriptors that claim requests by sigv4 signing name; rest-json dispatch then falls to JAX-RS path matching. Both MicroVM services sign as `lambda`, so their requests arrive under floci's lambda claim and route purely by path, and the MicroVM URI families (`/2025-09-09/`, `/2026-04-04/`, plus tags at `/2017-03-31/tags/`) collide with nothing the existing `LambdaController` serves.

CloudFormation provisioning has two plug points. The legacy path is a switch in `CloudFormationResourceProvisioner` calling services in-process. The newer path is the `CfnResourceProvisioner` interface with `CloudFormationResourceRegistry`, of which `SqsCfnProvisioner` is the model. New resource types go in the new way.

Upstream delivery follows the project's convention: a `[FEAT]` issue filed together with its PR, one pair per reviewable unit, conventional commits throughout.

## Status and the hedge

floci upstream is [`floci-io/floci`](https://github.com/floci-io/floci), Java, in-tree service modules, 18k stars. As of 2026-08-02 upstream `main` carries no MicroVM code. The module exists as a proposal: issue [#2078](https://github.com/floci-io/floci/issues/2078) and PR [#2079](https://github.com/floci-io/floci/pull/2079), both open. A build of the fork passes the `subset:floci` gate at 26 checks, 0 failures.

If floci upstream grows its own full MicroVM service before that lands, nothing is wasted. The conformance suite validates theirs the same way it validates m80, and m80 keeps its distribution niche as the small standalone target next to k3d.
