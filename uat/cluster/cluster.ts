/**
 * The UAT cluster, declared.
 *
 * `uat/up.sh` used to spell this as flags (`--agents 1`); the shape is data
 * now, faithful to what the script always created — loadbalancer included,
 * because trimming it would be a change, not a port. chant stamps its
 * ownership marker onto every node's Docker labels on the way through.
 *
 * The kubeconfig block restores k3d's own default behaviour explicitly:
 * merge into the default kubeconfig and switch to it. This harness is a
 * delete-and-recreate flow whose every kubectl call expects the ambient
 * context to be the cluster it just made — the switch is the point, so it
 * is declared rather than inherited.
 */
import { Cluster, KubeconfigOptions, Options } from "@intentius/chant-lexicon-k3d";

export const uatCluster = new Cluster({
  metadata: { name: "m80-uat" },
  servers: 1,
  agents: 1,
  options: new Options({
    kubeconfig: new KubeconfigOptions({
      updateDefaultKubeconfig: true,
      switchCurrentContext: true,
    }),
  }),
});
