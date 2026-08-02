# KubeMicroVM UAT harness

Scripts that run KubeMicroVM's own Robot Framework UAT suite on k3d against m80, instead of against the live EKS cluster and real AWS account it was written for.

```sh
just uat-up                                    # k3d + cert-manager + m80 + floci + operator
KUBEMICROVM=/path/to/KubeMicroVM just uat-run  # the suite
just uat-down
```

**The guide is [docs/kubemicrovm.md](../docs/kubemicrovm.md)** — prerequisites, what the stack is, every deviation from the upstream UAT and why, the pass matrix, and how to read a failure. It is on the docs site; this directory is just the scripts.

| File | What it is |
|---|---|
| `up.sh` | Brings the cluster and the whole stack up. Idempotent; re-running recreates the cluster |
| `run.sh` | Builds the runner image and runs the suite inside it |
| `Dockerfile` | The runner: Robot Framework, `kubectl`, the `microvm` CLI, the AWS CLI |
| `matrix.py` | Turns a Robot `output.xml` into the pass matrix table |

Every default in `up.sh` and `run.sh` is an environment variable, listed at the top of each script.
