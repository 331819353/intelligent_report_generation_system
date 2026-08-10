import { useCallback, useEffect, useRef, useState } from 'react'
import {
  mapAskDataError,
  questionAPI,
  subscribeQuestionEvents,
  type AskDataClientError,
  type QuestionEventSubscription,
  type QuestionOperation,
  type ReleaseDrift,
  type QuestionRun,
  type QuestionRunEvent,
  type QuestionStreamStatus,
} from '../lib/ask-data-api'

export type AskDataQuestionPhase =
  | 'IDLE' | 'CREATING' | 'CONNECTING' | 'STREAMING' | 'RECONNECTING'
  | 'TERMINAL' | 'ERROR' | 'CANCELED'

export type AskDataQuestionState = {
  phase: AskDataQuestionPhase
  operation?: QuestionOperation
  run?: QuestionRun
  events: QuestionRunEvent[]
  lastEventId: number
  error?: AskDataClientError
}

const initialState: AskDataQuestionState = { phase: 'IDLE', events: [], lastEventId: 0 }

// WEB-002 exposes the real Question API lifecycle without deciding how WEB-003
// renders it. Every invocation cancels the previous local request/stream, and
// generation guards prevent late async work from overwriting a newer run.
export function useAskDataQuestion() {
  const [state, setState] = useState<AskDataQuestionState>(initialState)
  const generationRef = useRef(0)
  const requestControllerRef = useRef<AbortController | null>(null)
  const subscriptionRef = useRef<QuestionEventSubscription | null>(null)
  const clarificationSubmittingRef = useRef(false)

  const stopActive = useCallback((markCanceled: boolean) => {
    generationRef.current += 1
    requestControllerRef.current?.abort()
    requestControllerRef.current = null
    subscriptionRef.current?.cancel()
    subscriptionRef.current = null
    if (markCanceled) {
      setState(current => ({ ...current, phase: 'CANCELED', error: undefined }))
    }
  }, [])

  useEffect(() => () => stopActive(false), [stopActive])

  const consumeOperation = useCallback(async (
    operation: QuestionOperation,
    generation: number,
    controller: AbortController,
  ): Promise<QuestionRun | undefined> => {
    if (generation !== generationRef.current) return undefined
    setState({ phase: 'CONNECTING', operation, events: [], lastEventId: 0 })
    const subscription = subscribeQuestionEvents(operation.runId, {
      signal: controller.signal,
      onStatus: (status: QuestionStreamStatus) => {
        if (generation !== generationRef.current) return
        const phase: AskDataQuestionPhase = status === 'RECONNECTING'
          ? 'RECONNECTING'
          : status === 'OPEN' ? 'STREAMING' : status === 'CONNECTING' ? 'CONNECTING' : 'STREAMING'
        setState(current => ({ ...current, phase }))
      },
      onEvent: event => {
        if (generation !== generationRef.current) return
        setState(current => {
          if (event.eventIndex <= current.lastEventId) return current
          return {
            ...current,
            events: [...current.events, event],
            lastEventId: event.eventIndex,
          }
        })
      },
    })
    subscriptionRef.current = subscription
    const outcome = await subscription.done
    if (generation !== generationRef.current || outcome.canceled) return undefined
    const run = await questionAPI.get(operation.runId, controller.signal)
    if (generation !== generationRef.current) return undefined
    setState(current => ({
      ...current,
      phase: run.completedAt ? 'TERMINAL' : 'STREAMING',
      run,
      lastEventId: Math.max(current.lastEventId, run.lastEventId),
      error: undefined,
    }))
    return run
  }, [])

  const begin = useCallback(async (
    create: (signal: AbortSignal) => Promise<QuestionOperation>,
    preserveCurrent = false,
  ): Promise<QuestionRun | undefined> => {
    stopActive(false)
    const generation = generationRef.current
    const controller = new AbortController()
    requestControllerRef.current = controller
    setState(current => preserveCurrent
      ? { ...current, phase: 'CREATING', error: undefined }
      : { phase: 'CREATING', events: [], lastEventId: 0 })
    try {
      const operation = await create(controller.signal)
      return await consumeOperation(operation, generation, controller)
    } catch (error) {
      if (generation !== generationRef.current) return undefined
      const mapped = mapAskDataError(error)
      if (mapped.kind === 'CANCELED') {
        setState(current => ({ ...current, phase: 'CANCELED', error: undefined }))
        return undefined
      }
      setState(current => ({ ...current, phase: 'ERROR', error: mapped }))
      return undefined
    } finally {
      if (generation === generationRef.current) {
        requestControllerRef.current = null
        subscriptionRef.current = null
      }
    }
  }, [consumeOperation, stopActive])

  const createQuestion = useCallback((question: string, conversationId?: string) => {
    clarificationSubmittingRef.current = false
    return begin(signal => questionAPI.create({ question, conversationId, signal }))
  }, [begin])

  const submitClarification = useCallback((optionId: string) => {
    const run = state.run
    const clarification = run?.completion?.clarification
    if (!run || run.state !== 'CLARIFICATION_REQUIRED' || !clarification || clarificationSubmittingRef.current) {
      return Promise.resolve(undefined)
    }
    clarificationSubmittingRef.current = true
    return begin(signal => questionAPI.clarify({
      runId: run.runId,
      clarificationId: clarification.clarificationId,
      optionId,
      runVersion: run.recordVersion,
      signal,
    }), true).finally(() => { clarificationSubmittingRef.current = false })
  }, [begin, state.run])

  const confirmReleaseDrift = useCallback((question: string, drift: ReleaseDrift) => {
    return begin(async signal => {
      await questionAPI.confirmReleaseDrift({
        conversationId: drift.conversationId,
        previousReleaseId: drift.previous.releaseId,
        activeReleaseId: drift.active.releaseId,
        signal,
      })
      return questionAPI.create({ question, conversationId: drift.conversationId, signal })
    }, true)
  }, [begin])

  const resumeQuestion = useCallback(async (runId: string): Promise<QuestionRun | undefined> => {
    clarificationSubmittingRef.current = false
    stopActive(false)
    const generation = generationRef.current
    const controller = new AbortController()
    requestControllerRef.current = controller
    setState({ phase: 'CONNECTING', events: [], lastEventId: 0 })
    try {
      const run = await questionAPI.get(runId, controller.signal)
      if (generation !== generationRef.current) return undefined
      if (run.completedAt) {
        setState({ phase: 'TERMINAL', run, events: [], lastEventId: run.lastEventId })
        return run
      }
      return await consumeOperation({
        runId: run.runId,
        conversationId: run.conversationId,
        state: run.state,
        replayed: true,
        eventsUrl: `/api/v1/questions/${run.runId}/events`,
      }, generation, controller)
    } catch (error) {
      if (generation !== generationRef.current) return undefined
      const mapped = mapAskDataError(error)
      if (mapped.kind === 'CANCELED') {
        setState(current => ({ ...current, phase: 'CANCELED', error: undefined }))
      } else {
        setState(current => ({ ...current, phase: 'ERROR', error: mapped }))
      }
      return undefined
    } finally {
      if (generation === generationRef.current) {
        requestControllerRef.current = null
        subscriptionRef.current = null
      }
    }
  }, [consumeOperation, stopActive])

  const cancel = useCallback(() => stopActive(true), [stopActive])
  const reset = useCallback(() => {
    clarificationSubmittingRef.current = false
    stopActive(false)
    setState(initialState)
  }, [stopActive])

  return { state, createQuestion, resumeQuestion, submitClarification, confirmReleaseDrift, cancel, reset }
}
