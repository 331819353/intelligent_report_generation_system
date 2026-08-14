/** Resolve the graph that the next conversational AI turn may modify. */
export function datasetAIRequestContext<T>(
  liveCanvas: T | undefined,
  stagedProposal: T | undefined,
  options: { forceLiveCanvas: boolean; stagedProposalApplied: boolean; preferStagedProposal?: boolean },
): T | undefined {
  if (options.forceLiveCanvas) return liveCanvas
  if (options.preferStagedProposal && !options.stagedProposalApplied && stagedProposal) return stagedProposal
  if (liveCanvas) return liveCanvas
  if (!options.stagedProposalApplied) return stagedProposal
  return undefined
}
