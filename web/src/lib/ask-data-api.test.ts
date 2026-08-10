import assert from 'node:assert/strict'
import test from 'node:test'

import { RequestError } from './api.ts'
import {
  AskDataClientError,
  SSEDecoder,
  buildClarificationSubmission,
  buildFeedbackSubmission,
  mapAskDataError,
  subscribeQuestionEvents,
  type QuestionRunState,
} from './ask-data-api.ts'

test('SSEDecoder handles chunk boundaries, comments, retry and multiline data', () => {
  const decoder = new SSEDecoder()
  const frames = [
    ...decoder.push('retry: 750\n\n: heart'),
    ...decoder.push('beat\r\nid: 1\r\nevent: question.run\r\ndata: {"a":'),
    ...decoder.push('1}\r\ndata: tail\r\n\r\n'),
    ...decoder.finish(),
  ]
  assert.deepEqual(frames, [
    { event: 'message', id: undefined, data: '', retry: 750 },
    { event: 'question.run', id: '1', data: '{"a":1}\ntail', retry: undefined },
  ])
})

test('question stream reconnects with Last-Event-ID and deduplicates replayed events', async () => {
  const runId = '11111111-1111-4111-8111-111111111111'
  const requests: Array<{ lastEventId?: string }> = []
  const responses = [
    responseFromText(`retry: 250\n\nid: 1\nevent: question.run\ndata: ${eventJSON(1, 'UNDERSTANDING')}\n\n`),
    responseFromText(
      `id: 1\nevent: question.run\ndata: ${eventJSON(1, 'UNDERSTANDING')}\n\n` +
      `id: 2\nevent: question.run\ndata: ${eventJSON(2, 'ANSWERED')}\n\n`,
    ),
  ]
	const received: Array<{ eventIndex: number; graphDegraded: boolean }> = []
  const statuses: string[] = []
  const subscription = subscribeQuestionEvents(runId, {
    maxReconnects: 2,
    request: async (_path, init) => {
      const headers = init?.headers as Record<string, string> | undefined
      requests.push({ lastEventId: headers?.['Last-Event-ID'] })
      const response = responses.shift()
      assert.ok(response)
      return response
    },
    wait: async () => undefined,
		onEvent: event => received.push({ eventIndex: event.eventIndex, graphDegraded: event.graphDegraded }),
    onStatus: status => statuses.push(status),
  })
  const outcome = await subscription.done
	assert.deepEqual(received, [
		{ eventIndex: 1, graphDegraded: false },
		{ eventIndex: 2, graphDegraded: true },
	])
  assert.deepEqual(requests, [{ lastEventId: undefined }, { lastEventId: '1' }])
  assert.equal(outcome.lastEventId, 2)
  assert.equal(outcome.terminal, true)
  assert.equal(outcome.canceled, false)
  assert.ok(statuses.includes('RECONNECTING'))
  assert.equal(statuses.at(-1), 'CLOSED')
})

test('question stream rejects event gaps without retrying contract corruption', async () => {
  const subscription = subscribeQuestionEvents('22222222-2222-4222-8222-222222222222', {
    request: async () => responseFromText(
      `id: 2\nevent: question.run\ndata: ${eventJSON(2, 'ANSWERED')}\n\n`,
    ),
    wait: async () => undefined,
  })
  await assert.rejects(subscription.done, (error: unknown) => {
    assert.ok(error instanceof AskDataClientError)
    assert.equal(error.code, 'QUESTION_STREAM_EVENT_GAP')
    assert.equal(error.retryable, false)
    return true
  })
})

test('question stream accepts governed answer verification and degradation events', async () => {
  const runId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
  const verifying = JSON.stringify({
    ...JSON.parse(eventJSON(1, 'ANSWER_VERIFYING')),
    code: 'ANSWER_VERIFICATION_FAILED', status: 'FAILED',
  })
  const degraded = JSON.stringify({
    ...JSON.parse(eventJSON(2, 'ANSWERED')),
    code: 'ANSWER_DEGRADED',
  })
  const received: string[] = []
  const subscription = subscribeQuestionEvents(runId, {
    request: async () => responseFromText(
      `id: 1\nevent: answer.verifying\ndata: ${verifying}\n\n` +
      `id: 2\nevent: answer.degraded\ndata: ${degraded}\n\n`,
    ),
    onEvent: event => received.push(event.code ?? ''),
  })
  const outcome = await subscription.done
  assert.deepEqual(received, ['ANSWER_VERIFICATION_FAILED', 'ANSWER_DEGRADED'])
  assert.equal(outcome.terminal, true)
})

test('question stream cancellation aborts the authenticated request locally', async () => {
  const subscription = subscribeQuestionEvents('33333333-3333-4333-8333-333333333333', {
    request: async (_path, init) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }),
  })
  subscription.cancel()
  const outcome = await subscription.done
  assert.equal(outcome.canceled, true)
  assert.equal(outcome.terminal, false)
})

test('server question errors map to stable client categories', () => {
  const mapped = mapAskDataError(new RequestError({
    code: 'QUESTION_SCOPE_CHANGED', message: 'internal message',
  }, 403))
  assert.equal(mapped.kind, 'SCOPE_CHANGED')
  assert.equal(mapped.code, 'QUESTION_SCOPE_CHANGED')
  assert.equal(mapped.retryable, false)
  assert.match(mapped.message, /权限或口径版本/)
})

test('release drift and expired clarification retain their structured recovery contract', () => {
  const conversationId = '77777777-7777-4777-8777-777777777777'
  const previousReleaseId = '88888888-8888-4888-8888-888888888888'
  const activeReleaseId = '99999999-9999-4999-8999-999999999999'
  const drift = mapAskDataError(new RequestError({
    code: 'RELEASE_DRIFT_CONFIRM_REQUIRED',
    message: 'internal message',
    releaseDrift: {
      conversationId,
      previous: {
        releaseId: previousReleaseId,
        contentHash: 'a'.repeat(64),
        semanticVersion: '2026.08',
        status: 'SUPERSEDED',
      },
      active: {
        releaseId: activeReleaseId,
        contentHash: 'b'.repeat(64),
        semanticVersion: '2026.08.1',
        status: 'ACTIVE',
      },
      changes: [{
        objectType: 'METRIC',
        objectId: 'metric:sales',
        name: '销售额',
        changeKind: 'UPDATED',
        summary: '计算逻辑已更新',
      }],
    },
  }, 409))
  assert.equal(drift.kind, 'RELEASE_DRIFT')
  assert.equal(drift.releaseDrift?.conversationId, conversationId)
  assert.equal(drift.releaseDrift?.active.releaseId, activeReleaseId)
  assert.equal(drift.releaseDrift?.changes[0]?.objectType, 'METRIC')

  const expired = mapAskDataError(new RequestError({
    code: 'CLARIFICATION_EXPIRED', message: 'internal message',
  }, 409))
  assert.equal(expired.kind, 'CLARIFICATION_EXPIRED')
  assert.equal(expired.retryable, false)
  assert.match(expired.message, /澄清已超时/)
})

test('clarification submission binds artifact identity and optimistic run version', () => {
  const submission = buildClarificationSubmission({
    runId: '44444444-4444-4444-8444-444444444444',
    clarificationId: '55555555-5555-4555-8555-555555555555',
    optionId: '  clarification-option:paid-sales  ',
    runVersion: 12,
  })
  assert.deepEqual(submission, {
    runId: '44444444-4444-4444-8444-444444444444',
    clarificationId: '55555555-5555-4555-8555-555555555555',
    optionId: 'clarification-option:paid-sales',
    runVersion: 12,
  })
  assert.throws(() => buildClarificationSubmission({ ...submission, runVersion: 0 }), (error: unknown) => {
    assert.ok(error instanceof AskDataClientError)
    assert.equal(error.code, 'QUESTION_RUN_VERSION_INVALID')
    return true
  })
})

test('feedback submission binds the terminal run and enforces structured issue shape', () => {
  const submission = buildFeedbackSubmission({
    runId: '66666666-6666-4666-8666-666666666666',
    runVersion: 18,
    rating: 'INACCURATE',
    issueType: 'METRIC',
    comment: '  指标口径未扣除退款  ',
  })
  assert.deepEqual(submission, {
    runId: '66666666-6666-4666-8666-666666666666',
    runVersion: 18,
    rating: 'INACCURATE',
    issueType: 'METRIC',
    comment: '指标口径未扣除退款',
  })
  assert.throws(() => buildFeedbackSubmission({ ...submission, issueType: 'NONE' }), (error: unknown) => {
    assert.ok(error instanceof AskDataClientError)
    assert.equal(error.code, 'QUESTION_FEEDBACK_SHAPE_INVALID')
    return true
  })
  assert.throws(() => buildFeedbackSubmission({ ...submission, comment: 'line\nbreak' }), (error: unknown) => {
    assert.ok(error instanceof AskDataClientError)
    assert.equal(error.code, 'QUESTION_FEEDBACK_COMMENT_INVALID')
    return true
  })
})

function responseFromText(text: string): Response {
  const bytes = new TextEncoder().encode(text)
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      const split = Math.max(1, Math.floor(bytes.length / 2))
      controller.enqueue(bytes.slice(0, split))
      controller.enqueue(bytes.slice(split))
      controller.close()
    },
  }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
}

function eventJSON(eventIndex: number, state: QuestionRunState): string {
  return JSON.stringify({
    eventId: `00000000-0000-4000-8000-${String(eventIndex).padStart(12, '0')}`,
    eventIndex,
    runVersion: eventIndex,
    state,
    type: 'STATE_TRANSITION',
    stage: state,
    status: state === 'ANSWERED' ? 'SUCCEEDED' : 'STARTED',
    code: state === 'ANSWERED' ? 'ANSWER_READY' : `STATE_${state}`,
    evidenceIds: [],
		graphDegraded: state === 'ANSWERED',
    createdAt: '2026-08-06T10:00:00Z',
  })
}
