# Review policy

## Scope and attribution

- Review the current PR head against its actual declared base, using their merge
  base where appropriate. Verify the revisions; stacked PRs are incremental.
- Inspect inherited code when needed to understand an interaction, but distinguish
  pre-existing defects from defects introduced or newly exposed by this increment.
- Check earlier stack findings and existing project issues before reporting a
  duplicate. Link the original finding and note any new trigger or changed impact.
- Report one finding per root cause; combine symptoms that share the same fix.
- Do not turn a focused review into an unrelated architecture or style rewrite.

## Classification

**Affected PR finding:** a concrete correctness, security, data-loss,
compatibility, build, or material performance defect attributable to the increment.
Explain the conditions under which it matters; avoid speculative failure claims.

**Project backlog feedback:** purely non-runtime documentation, style, general
test-coverage, or refactoring feedback. Collect it in the Linear project backlog
for collective review after the stack rather than blocking the current increment.

Classify by demonstrated impact, not by file type. A missing test that reveals a
concrete defect belongs with that defect on the affected PR. Unsafe operational
instructions and documentation that causes incorrect behavior are not exempt
just because they are documentation. General requests for more tests go to backlog.

**Unverified concern:** plausible risk without enough evidence to call a defect.
State what is unknown and the smallest check that could establish the impact.
Mark an already-covered duplicate as such and link it instead of opening another
finding. An inherited defect stays attributed to its originating change unless
this increment creates a distinct actionable trigger or worsens its impact.

## Finding evidence

For each finding or backlog item, give the classification and:
- the affected file/location and relevant revision;
- the concrete trigger, expected behavior, and observed or reasoned impact;
- supporting code, reproduction, test, or operational evidence;
- assumptions, uncertainty, and any verification not performed;
- an existing finding/issue link when the root cause is already tracked.

Do not invent acceptance thresholds or treat a successful command as proof of
every compatibility or performance claim. If there are no actionable PR defects,
say so; keep any separately classified backlog feedback visible.

## Conditional media and configuration checks

When the change touches these paths, check the relevant invariants:
- Budgets are cancellable and acquired at leaf operations; nested acquisition
  cannot deadlock. Failure/fallback/cancellation releases permits and children.
- Saved configuration changes atomically; persisted requests remain distinct from
  the active configuration snapshot and its documented activation behavior.
- CPU fallback preserves canonical seek, pixel conversion, output, and hash
  semantics. Stored hashes and algorithm changes require their own evidence.
- Frame compatibility uses actual source pixels and CPU controls, not merely
  matching PTS, VTT bytes, backend labels, or observed GPU activity.

When the increment adds detailed budget, configuration, benchmark, or hardware
acceptance documentation, use those documents for applicable checks. Do not assume
planned media features or acceptance gates already exist on the declared base.
For general verification, see [development](docs/DEVELOPMENT.md).

## Review handoff

Reviewers return classifications and evidence. The coordinating workflow creates
or deduplicates Linear issues when authorized; reviewer output is not automatic
permission to write the tracker. This policy grants no implementation, Git,
merge, deployment, host-mutation, or credential permissions.
