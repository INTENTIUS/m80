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

floci upstream is `floci-io/floci`, Java, in-tree service modules, 18k stars, no MicroVM code or issues as of 2026-07-29. The contribution path is proven by the in-flight bedrock-agentcore work in the lex00 fork, which is the same shape, a 2026 AWS service added as a service module.

## Asymmetric scope

The floci module implements the subset CFN provisioning needs. Image create, get, delete, build lifecycle enough for stack create and delete to converge, network connectors (`MicrovmApp` emits `AWS::Lambda::NetworkConnector`), and basic VM CRUD only if `AWS::Lambda::Microvm` ever becomes a CFN type. It does not need tokens, endpoint stubs, idle timers, or drift levers. Scoping it narrow keeps the second implementation cheap and the drift surface small.

## Sequencing

m80 first. It unblocks the kubemicrovm-ops kit, the operator community, and behold demos, all of which ride the raw API. The floci contribution follows when the `MicrovmApp` local path is actually wanted, reusing the conformance suite's tagged subset as its acceptance gate.

## The hedge

If floci upstream grows its own full MicroVM service before the contribution lands, nothing is wasted. The conformance suite validates theirs the same way it validates m80, and m80 keeps its distribution niche as the small standalone target next to k3d.
