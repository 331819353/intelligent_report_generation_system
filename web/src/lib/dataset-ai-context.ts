export type DatasetAICanvasInventory = {
  nodes: number
  joins: number
  groups: number
  transforms: number
  hasEnd: boolean
}

/** Only a completely empty canvas starts the guided CREATE intake. */
export function datasetAICanvasMode(inventory: DatasetAICanvasInventory): 'CREATE' | 'MODIFY' {
  return inventory.nodes + inventory.joins + inventory.groups + inventory.transforms + Number(inventory.hasEnd) > 0
    ? 'MODIFY'
    : 'CREATE'
}

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
