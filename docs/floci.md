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
