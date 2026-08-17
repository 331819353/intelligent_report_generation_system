import { apiRequest, apiResponse, RequestError, type APIError } from './api.ts'

export type DatasetAIProgressEvent = {
  timestamp: string
  stage: 'CONTEXT' | 'CATALOG' | 'INTENT' | 'PLANNER' | 'VALIDATION' | 'REPAIR' | 'COMPLETE'
  status: 'RUNNING' | 'SUCCEEDED' | 'WARN'
  message: string
}

/**
 * Read an NDJSON progress stream from a dataset AI endpoint. Progress frames are
 * forwarded; the single result frame is returned; an error frame becomes a
 * RequestError exactly like the non-streaming response would. Kept as a leaf
 * module (extension imports only) so session tests can load it under node --test.
 */
export async function requestDatasetAIStream<TResult>(
  path: string,
  init: RequestInit,
  resultKey: 'result' | 'session',
  onProgress?: (event: DatasetAIProgressEvent) => void,
): Promise<TResult> {
  if (!onProgress) return apiRequest<TResult>(path, init)
  const response = await apiResponse(path, {
    ...init,
    headers: { ...(init.headers ?? {}), Accept: 'application/x-ndjson' },
  })
  if (!response.body || !response.headers?.get('Content-Type')?.includes('application/x-ndjson')) {
    return response.json() as Promise<TResult>
  }
  type StreamFrame =
    | { type: 'progress'; progress: DatasetAIProgressEvent }
    | ({ type: 'result' } & Record<string, unknown>)
    | { type: 'error'; status: number; error: APIError }
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let result: TResult | undefined
  const consume = (line: string) => {
    if (!line.trim()) return
    const frame = JSON.parse(line) as StreamFrame
    if (frame.type === 'progress') onProgress(frame.progress)
    if (frame.type === 'result') result = frame[resultKey] as TResult
    if (frame.type === 'error') throw new RequestError(frame.error, frame.status)
  }
  while (true) {
    const chunk = await reader.read()
    buffer += decoder.decode(chunk.value, { stream: !chunk.done })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) consume(line)
    if (chunk.done) break
  }
  consume(buffer)
  if (result === undefined) {
    throw new RequestError({ code: 'AI_STREAM_INCOMPLETE', message: 'AI 生成连接提前结束，请重试' }, 502)
  }
  return result
}
