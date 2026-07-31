# Division of labor with floci

## Architecture findings (2026-07-29)

Research against the local floci fork, recorded so the implementation issues stand alone.

Floci is Quarkus. Services are JAX-RS controllers registered in `ResolvedServiceCatalog` as descriptors that claim requests by sigv4 signing name; rest-json dispatch then falls to JAX-RS path matching. Both MicroVM services sign as `lambda`, so their requests arrive under floci's lambda claim and route purely by path, and the MicroVM URI families (`/2025-09-09/`, `/2026-04-04/`, plus tags at `/2017-03-31/tags/`) collide with nothing the existing `LambdaController` serves. The bedrock-agentcore branch in the fork is the recipe for the whole shape: controllers, service, model classes, catalog descriptor, config toggle, tests, in reviewable conventional commits.

CloudFormation provisioning has two plug points. The legacy path is a switch in `CloudFormationResourceProvisioner` calling services in-process. The newer path is the `CfnResourceProvisioner` interface with `CloudFormationResourceRegistry` (`SqsCfnProvisioner` is the model). New resource types go in the new way.

Scope correction to the split below: chant's `MicrovmApp` emits `AWS::Lambda::NetworkConnector` when VPC egress is requested, so the CFN-sufficient subset includes connectors, not just `MicrovmImage`. The conformance suite's `subset:floci` tag covers connectors accordingly.

Delivery constraints. Nothing is pushed to the fork or upstream until the work is complete and green locally. Upstream delivery follows the fork's established convention: a `[FEAT]` issue (Service / API Action / AWS documentation / why / willing-to-PR template) filed together with its PR, one pair per reviewable unit. Conventional commits throughout. The fork-only `publish-docker.yml` (manual dispatch, any ref, multi-arch to GHCR) publishes a testing image at delivery time, not before.

## The split

| Concern | Home | Why |
|---------|------|-----|
| Full-fidelity service emulation | m80 | Owned cadence against a churning preview-fresh API, small container the KubeMicroVM community can adopt, mudflaps mold |
| `AWS::Lambda::MicrovmImage` through CloudFormation | floci | CFN emulation can only live where the CFN engine lives. chant's `MicrovmApp` and the kit's image stack deploy through CFN |
| Conformance contract | shared suite | See [conformance.md](conformance.md) |

floci upstream is `floci-io/floci`, Java, in-tree service modules, 18k stars, and still carries no MicroVM code and no MicroVM issues — re-checked 2026-07-31, so the hedge below is intact. The contribution path is proven by the in-flight bedrock-agentcore work in the lex00 fork, which is the same shape, a 2026 AWS service added as a service module.

## Asymmetric scope

The floci module implements the subset CFN provisioning needs. Image create, get, delete, build lifecycle enough for stack create and delete to converge, network connectors (`MicrovmApp` emits `AWS::Lambda::NetworkConnector`), and basic VM CRUD only if `AWS::Lambda::Microvm` ever becomes a CFN type. It does not need tokens, endpoint stubs, idle timers, or drift levers. Scoping it narrow keeps the second implementation cheap and the drift surface small.

## Sequencing

m80 first. It unblocks the kubemicrovm-ops kit, the operator community, and behold demos, all of which ride the raw API. The floci contribution follows when the `MicrovmApp` local path is actually wanted, reusing the conformance suite's tagged subset as its acceptance gate.

## The hedge

If floci upstream grows its own full MicroVM service before the contribution lands, nothing is wasted. The conformance suite validates theirs the same way it validates m80, and m80 keeps its distribution niche as the small standalone target next to k3d.

## What the module owes the suite

The module is gated on the load-bearing set only, per [conformance.md](conformance.md); m80 aims at both because exactness is nearly free for a purpose-built emulator.

### The score cannot be read off a single run

An earlier revision of this page reported "34 diverging steps at `-tier all`, 27 at `-tier load-bearing`". **That number is not reproducible and should not be planned against.** Re-measured 2026-07-31 against `feat/lambda-microvms` at `e6822a79`, with floci built as a fast-jar and the current suite pointed at it:

```
pass 0, fail 3, unimplemented 0, skipped 23
```

Three scenarios, each failing on its **first** step, and 23 steps that never ran. The runner halts a scenario when a step fails, because every later step assumes the state the failed one was supposed to create — so one divergence in `create` hides every divergence behind it. A count of diverging steps cannot be taken from one run at all; it is discovered one fix at a time, and the true figure is only ever a lower bound until the scenario runs to the end.

The cluster table below came from reading the four response builders, not from the runner, so it stands. What does not stand is any claim to know how many steps diverge.

### What blocks the first step

`images-lifecycle/create` returns 14 of the fixture's 24 members. Ten are missing, and the tiering splits them cleanly:

| Missing member | Tier |
|---|---|
| `resources`, `egressNetworkConnectors` | load-bearing |
| `id`, `additionalOsCapabilities`, `buildPhaseOverrides`, `cpuConfigurations`, `environmentVariables`, `hooks`, `logging`, `roleConfiguration` | cosmetic |

So at the gate that matters, **two members on one response builder** are what stand between floci and twelve currently unmeasurable steps in `images-lifecycle` alone. That is the place to start, and it is smaller than the old headline number made it sound.

None of this is 27 problems, or 3, or any fixed count. The data already exists on the domain model and is simply never written to the response, so it is serialization work rather than state work.

### Where it lives

Four private methods build every response: `imageNode`, `versionNode` and `buildNode` in `LambdaMicrovmsController`, and `connectorNode` in `LambdaNetworkConnectorsController`.

Ordered by what unblocks the most measurement, not by a step count — see above for why a step count is not available.

| Cluster | Change |
|---------|--------|
| Image projections | `imageNode(image, create)` models two shapes through a boolean; live has three. Create returns full detail, Get returns a *smaller* summary, List smaller again. Create is also the step every image scenario dies on |
| Version detail | `versionNode` returns 5 members against ~14 load-bearing. The spec fields come from the parent image, which the method already receives |
| Connectors | `AssociatedComputeResourceTypes` and `NetworkProtocol` are request fields the module validates and then discards. The list envelope is `Items` where live sends `NetworkConnectors`, and list items are a distinct six-member shape rather than the full node |
| Builds | One build per version where live mints two, Graviton 4 and 3; missing `chipsetGeneration` and `snapshotBuild` on Get |
| Behavioural | `latestActiveImageVersion` is set while the image is still `CREATING`; the update path settles to `CREATED` instead of walking `UPDATING` → `UPDATED`; `idlePolicy` is dropped from VM responses |

Eight model fields are missing and everything else is present but unserialized: `MicrovmImage.id`, `MicrovmImageVersion.updatedAt`, `MicrovmBuild.chipsetGeneration`, `NetworkConnector.{networkProtocol, associatedComputeResourceTypes, type, lastModified}`, `Microvm.idlePolicy`.

Roughly 150 lines of Java plus test updates — an estimate from reading the four builders, not a measurement.

### Why the sparse bodies matter most

The KubeMicroVM operator runs its own drift detection, which works by reading back what it wrote and comparing. A service that does not echo `codeArtifact`, `buildRoleArn`, `baseImageArn`, `description` and `tags` looks permanently drifted: the reconciler writes, re-reads, sees a difference, writes again. That is a reconcile loop that never converges, and it is the first thing anyone pointing an operator at the module would hit. `latestActiveImageVersion` set on a `CREATING` image is the same class — a reconciler reads it and launches a VM off a version that does not exist yet.

### Acceptance

1. `-tier load-bearing` across the floci-relevant scenarios: 0 fail, and every step actually running rather than skipped behind an earlier failure
2. The module's own test suite green, 649 tests across the lambda, lambdamicrovms and cloudformation packages
3. `CloudFormationLambdaMicrovmsIntegrationTest` green — this is the gate that matters, since it drives both CFN types through the provisioners end to end

Gate 3 was very nearly written as "chant's `MicrovmApp` deploys against floci", the #31 acceptance. That would have been wrong. Nothing here depends on chant: m80's `go.mod` is `aws-sdk-go-v2` alone, the floci module has no chant reference, and the CFN integration test builds its template as an inline string. chant is a *consumer* — the provisioners exist because `MicrovmApp` emits these types, but CloudFormation is the interface between them and any template using those types exercises the same path. Reaching for the chant end-to-end would have added an npm install, a pinned lexicon version and a live chant-side bug ([chant#1219](https://github.com/INTENTIUS/chant/issues/1219)) to the gate for a change that only rewrites JSON serialization.

### Deliberately out

The seven cosmetic steps stay diverging; that is what the tiering decided. Tokens, endpoint stubs, suspend and resume stay m80's per the asymmetric scope above. The always-null members are one `putNull` each and could be added for free — worth doing only if a complete-looking response shape helps the upstream review more than the extra diff hurts it.

Three commits, matching the clusters, so [#32](https://github.com/INTENTIUS/m80/issues/32) has reviewable units: image and version projections, connector projections and envelope, behavioural fixes.
