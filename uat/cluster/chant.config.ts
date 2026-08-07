import type { ChantConfig } from "@intentius/chant";

/**
 * The smallest chant project that can exist: one declarable, one lexicon.
 *
 * m80 itself is a single Go binary with no companions, and stays that way —
 * this directory is the UAT *harness's* infrastructure, not m80's. The
 * harness's k3d cluster was the one piece of it spelled as CLI flags; now
 * the flags are data and `k3d cluster create --config` consumes what
 * `chant build` emits. node/npm join the harness prerequisites (they were
 * already nowhere near the binary), and the emitted YAML is a file the
 * native tool accepts with chant nowhere in sight.
 */
export default {
  lexicons: ["k3d"],
  sourceDir: ".",
  ownership: { stack: "m80-uat", env: "uat" },
} satisfies ChantConfig;
