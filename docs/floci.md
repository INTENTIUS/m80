# Division of labor with floci

## The split

| Concern | Home | Why |
|---------|------|-----|
| Full-fidelity service emulation | m80 | Owned cadence against a churning preview-fresh API, small container the KubeMicroVM community can adopt, mudflaps mold |
| `AWS::Lambda::MicrovmImage` through CloudFormation | floci | CFN emulation can only live where the CFN engine lives. chant's `MicrovmApp` and the kit's image stack deploy through CFN |
| Conformance contract | shared suite | See [conformance.md](conformance.md) |

floci upstream is `floci-io/floci`, Java, in-tree service modules, 18k stars, no MicroVM code or issues as of 2026-07-29. The contribution path is proven by the in-flight bedrock-agentcore work in the lex00 fork, which is the same shape, a 2026 AWS service added as a service module.

## Asymmetric scope

The floci module implements the subset CFN provisioning needs. Image create, get, delete, build lifecycle enough for stack create and delete to converge, basic VM CRUD if `AWS::Lambda::Microvm` ever becomes a CFN type. It does not need tokens, endpoint stubs, idle timers, or drift levers. Scoping it narrow keeps the second implementation cheap and the drift surface small.

## Sequencing

m80 first. It unblocks the kubemicrovm-ops kit, the operator community, and behold demos, all of which ride the raw API. The floci contribution follows when the `MicrovmApp` local path is actually wanted, reusing the conformance suite's tagged subset as its acceptance gate.

## The hedge

If floci upstream grows its own full MicroVM service before the contribution lands, nothing is wasted. The conformance suite validates theirs the same way it validates m80, and m80 keeps its distribution niche as the small standalone target next to k3d.

## What the module owes the suite

Scored 2026-07-30 against the recorded fixtures: **34 diverging steps at `-tier all`, 27 at `-tier load-bearing`**. The seven that fall away are decoration — always-null config knobs, the leaked `versionStateTimeBucket` index key, `__type` on error bodies, message wording. The module is gated on the load-bearing set only, per [conformance.md](conformance.md); m80 aims at both because exactness is nearly free for a purpose-built emulator.

The 27 are not 27 problems. The data already exists on the domain model and is simply never written to the response, so this is serialization work rather than state work.

### Where it lives

Four private methods build every response: `imageNode`, `versionNode` and `buildNode` in `LambdaMicrovmsController`, and `connectorNode` in `LambdaNetworkConnectorsController`.

| Cluster | Change | Steps |
|---------|--------|-------|
| Image projections | `imageNode(image, create)` models two shapes through a boolean; live has three. Create returns full detail, Get returns a *smaller* summary, List smaller again | ~14 |
| Version detail | `versionNode` returns 5 members against ~14 load-bearing. The spec fields come from the parent image, which the method already receives | shared |
| Connectors | `AssociatedComputeResourceTypes` and `NetworkProtocol` are request fields the module validates and then discards. The list envelope is `Items` where live sends `NetworkConnectors`, and list items are a distinct six-member shape rather than the full node | ~8 |
| Builds | One build per version where live mints two, Graviton 4 and 3; missing `chipsetGeneration` and `snapshotBuild` on Get | 2 |
| Behavioural | `latestActiveImageVersion` is set while the image is still `CREATING`; the update path settles to `CREATED` instead of walking `UPDATING` → `UPDATED`; `idlePolicy` is dropped from VM responses | ~9 |

Eight model fields are missing and everything else is present but unserialized: `MicrovmImage.id`, `MicrovmImageVersion.updatedAt`, `MicrovmBuild.chipsetGeneration`, `NetworkConnector.{networkProtocol, associatedComputeResourceTypes, type, lastModified}`, `Microvm.idlePolicy`.

Roughly 150 lines of Java plus test updates.

### Why the sparse bodies matter most

The KubeMicroVM operator runs its own drift detection, which works by reading back what it wrote and comparing. A service that does not echo `codeArtifact`, `buildRoleArn`, `baseImageArn`, `description` and `tags` looks permanently drifted: the reconciler writes, re-reads, sees a difference, writes again. That is a reconcile loop that never converges, and it is the first thing anyone pointing an operator at the module would hit. `latestActiveImageVersion` set on a `CREATING` image is the same class — a reconciler reads it and launches a VM off a version that does not exist yet.

### Acceptance

1. `-tier load-bearing` across the floci-relevant scenarios: 0 fail, from 27
2. The module's own test suite green, 649 tests across the lambda, lambdamicrovms and cloudformation packages
3. `CloudFormationLambdaMicrovmsIntegrationTest` green — this is the gate that matters, since it drives both CFN types through the provisioners end to end

Gate 3 was very nearly written as "chant's `MicrovmApp` deploys against floci", the #31 acceptance. That would have been wrong. Nothing here depends on chant: m80's `go.mod` is `aws-sdk-go-v2` alone, the floci module has no chant reference, and the CFN integration test builds its template as an inline string. chant is a *consumer* — the provisioners exist because `MicrovmApp` emits these types, but CloudFormation is the interface between them and any template using those types exercises the same path. Reaching for the chant end-to-end would have added an npm install, a pinned lexicon version and a live chant-side bug ([chant#1219](https://github.com/INTENTIUS/chant/issues/1219)) to the gate for a change that only rewrites JSON serialization.

### Deliberately out

The seven cosmetic steps stay diverging; that is what the tiering decided. Tokens, endpoint stubs, suspend and resume stay m80's per the asymmetric scope above. The always-null members are one `putNull` each and could be added for free — worth doing only if a complete-looking response shape helps the upstream review more than the extra diff hurts it.

Three commits, matching the clusters, so [#32](https://github.com/INTENTIUS/m80/issues/32) has reviewable units: image and version projections, connector projections and envelope, behavioural fixes.
