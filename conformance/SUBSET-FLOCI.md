# The floci subset

The `subset:floci` slice of this suite is the acceptance gate for a CloudFormation-sufficient MicroVMs implementation — one that provisions `AWS::Lambda::MicrovmImage` and `AWS::Lambda::NetworkConnector` and nothing more. It is the narrower of the two bars this suite holds targets to; see [conformance.md](../docs/conformance.md) for why the bars differ.

It exists to be run from somewhere else. Nothing here is m80-specific: point it at any endpoint that claims to serve the API.

## Running it

From a clean checkout, against any endpoint:

```sh
go run ./conformance/cmd/conformance \
    -endpoint http://localhost:4566 \
    -tags subset:floci \
    -tier load-bearing \
    -poll-timeout 20
```

Or `just subset-floci http://localhost:4566`.

The exit status is the result: `0` when every step passed, non-zero otherwise. Each step prints its own pass/fail line, so a CI log shows which one broke rather than only that something did.

| Flag | Why |
|---|---|
| `-tags subset:floci` | Restricts to the three CFN-relevant scenarios |
| `-tier load-bearing` | Ignores members nothing branches on. A CFN-sufficient target is not held to always-null decoration |
| `-poll-timeout 20` | Case timeouts are sized for real AWS, where a build takes 45 minutes. Without this an emulator run waits them out |
| `-keep-going` | Optional. Reports every divergence in one run rather than the first, for bringing a target up. Not for CI |

Targets are stateful and most reserve resource names through an asynchronous delete window, so a second run against the same instance will fail on `already exists`. Restart between runs.

A worked example: `ghcr.io/lex00/floci:microvm` passes this subset at 26 checks, 0 failures.

## What each case backs

Three scenarios, 26 steps. Every one exists because a CloudFormation behavior depends on it.

### `images-lifecycle` → `AWS::Lambda::MicrovmImage`

The resource's whole lifecycle, plus the version and build machinery a stack create must wait on.

| Step | Operation | CFN behavior it backs |
|---|---|---|
| `create` | `CreateMicrovmImage` | Stack create provisions the resource |
| `get-until-created` | `GetMicrovmImage` | Stack create polls to `CREATE_COMPLETE` |
| `list-images` | `ListMicrovmImages` | Drift detection and physical-id resolution |
| `version-until-built` | `GetMicrovmImageVersion` | A stack cannot report complete until a version is built; this is the poll that decides |
| `list-versions` | `ListMicrovmImageVersions` | Resolving which version a stack update produced |
| `list-builds` | `ListMicrovmImageBuilds` | Build progress a stack event can surface |
| `get-build` | `GetMicrovmImageBuild` | Per-attempt failure detail when a stack create fails |
| `update-image` | `UpdateMicrovmImage` | Stack update on a changed template |
| `get-until-updated` | `GetMicrovmImage` | Stack update polls to `UPDATE_COMPLETE` |
| `update-version-status` | `UpdateMicrovmImageVersion` | Version status is an update side effect |
| `delete-version` | `DeleteMicrovmImageVersion` | Version cleanup during update and delete |
| `image-until-settled` | `GetMicrovmImage` | Delete is asynchronous; the stack waits |
| `delete-image` | `DeleteMicrovmImage` | Stack delete removes the resource |

### `vms-lifecycle` → stack-delete convergence

**This scenario is in the subset even though there is no `AWS::Lambda::Microvm` CFN type**, which is worth stating because the obvious reading is that it should not be.

`DeleteMicrovmImage` refuses while a non-terminated MicroVM references the image — recorded live, `400 "Cannot delete microvm image with running microvms."` So a stack delete that removes an image cannot converge unless the target models MicroVM state well enough to know the image is free. A target implementing images alone will pass every image step and then hang or fail a stack delete in the field.

| Step | Operation | CFN behavior it backs |
|---|---|---|
| `create-image`, `version-until-built` | image setup | Preconditions for the rest |
| `run` | `RunMicrovm` | Creates the reference that blocks image delete |
| `get-until-running` | `GetMicrovm` | The state the delete check reads |
| `list` | `ListMicrovms` | How a target finds references to an image |
| `terminate` | `TerminateMicrovm` | Releases the reference |
| `get-until-terminated` | `GetMicrovm` | Terminate is asynchronous; the release is not immediate |
| `delete-image` | `DeleteMicrovmImage` | The delete that must now succeed |

### `connectors` → `AWS::Lambda::NetworkConnector`

chant's `MicrovmApp` emits this type when VPC egress is requested, so it is CFN-sufficient scope rather than an extra.

| Step | Operation | CFN behavior it backs |
|---|---|---|
| `create` | `CreateNetworkConnector` | Stack create provisions the resource |
| `get-until-active` | `GetNetworkConnector` | Stack create polls to `ACTIVE` |
| `list` | `ListNetworkConnectors` | Drift detection and physical-id resolution |
| `update` | `UpdateNetworkConnector` | Stack update on a changed configuration |
| `delete` | `DeleteNetworkConnector` | Stack delete removes the resource |

## What is deliberately out

Auth tokens, the per-VM endpoint, idle and suspend timers, suspend and resume, tags, and the managed base-image catalog. None is reachable through a CloudFormation template, so holding a CFN-sufficient target to them would be asking for work no consumer of that target can use. m80 implements all of them and is held to `-tier all` instead.
