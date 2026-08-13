export type AskDataAttachmentDraftItem = {
  id: string
  name: string
  size: number
  type: string
  excerpt: string
}

export const askDataAttachmentLimit = 3
export const askDataAttachmentMaxBytes = 1024 * 1024
export const askDataAttachmentAccept = '.txt,.csv,.json,.md,text/plain,text/csv,application/json,text/markdown'

const storageKey = 'ask-data:attachment-draft'
const maxExcerptLength = 900
const maxContextLength = 3000

function draftStorage() {
  return typeof window === 'undefined' ? undefined : window.sessionStorage
}

export function saveAskDataAttachmentDraft(items: AskDataAttachmentDraftItem[]) {
  const storage = draftStorage()
  if (!storage) return
  if (items.length === 0) {
    storage.removeItem(storageKey)
    return
  }
  storage.setItem(storageKey, JSON.stringify(items.slice(0, askDataAttachmentLimit)))
}

export function readAskDataAttachmentDraft(): AskDataAttachmentDraftItem[] {
  const storage = draftStorage()
  if (!storage) return []
  try {
    const value: unknown = JSON.parse(storage.getItem(storageKey) || '[]')
    if (!Array.isArray(value)) return []
    return value.flatMap(item => {
      if (!item || typeof item !== 'object') return []
      const record = item as Record<string, unknown>
      if (typeof record.id !== 'string' || typeof record.name !== 'string' || typeof record.excerpt !== 'string') return []
      return [{
        id: record.id.slice(0, 240),
        name: record.name.slice(0, 180),
        size: typeof record.size === 'number' && Number.isFinite(record.size) ? Math.max(0, record.size) : 0,
        type: typeof record.type === 'string' ? record.type.slice(0, 120) : '',
        excerpt: record.excerpt.slice(0, maxExcerptLength),
      }]
    }).slice(0, askDataAttachmentLimit)
  } catch {
    storage.removeItem(storageKey)
    return []
  }
}

export function clearAskDataAttachmentDraft() {
  draftStorage()?.removeItem(storageKey)
}

export function buildAskDataAttachmentContext(items: AskDataAttachmentDraftItem[]) {
  if (items.length === 0) return ''
  const context = items.map(item => `附件：${item.name}\n${item.excerpt}`).join('\n\n')
  return `以下是用户为本次分析上传的附件文本，请仅将其作为补充上下文：\n${context}`.slice(0, maxContextLength)
}
