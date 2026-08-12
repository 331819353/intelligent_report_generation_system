const nonVersionedResources = new Set(['metrics'])

export function semanticCreatePayload(resource: string, payload: Record<string, unknown>) {
  if (nonVersionedResources.has(resource)) return payload
  return { ...payload, versionNo: typeof payload.versionNo === 'number' ? payload.versionNo : 1 }
}
