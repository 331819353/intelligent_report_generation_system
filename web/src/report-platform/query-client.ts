import { useEffect, useMemo, useState } from "react";
import { queryDraftReport, queryPublishedReport } from "./api";
import { reportCardRegistry } from "./registry";
import type {
  CardInteractionEvent,
  CardQueryResult,
  ReportDefinition,
  ReportInteractionContext,
} from "./types";

export type ReportQueryScope =
  | { kind: "draft"; reportId: string; revision: number }
  | { kind: "published"; reportId: string; version: number };

export function useReportQueries(
  scope: ReportQueryScope | undefined,
  definition: ReportDefinition,
  filters: Record<string, unknown>,
  interactionContext: Record<string, ReportInteractionContext>,
) {
  const cardIds = useMemo(
    () =>
      definition.cards
        .filter((card) => reportCardRegistry.get(card.type)?.buildQuery(card))
        .map((card) => card.id),
    [definition.cards],
  );
  const fingerprint = JSON.stringify({
    scope,
    cardIds,
    filters,
    interactionContext,
    definition: definition.cards.map((card) => [card.id, card.binding]),
  });
  const [state, setState] = useState<{
    fingerprint: string;
    loading: boolean;
    results: Record<string, CardQueryResult>;
    error?: string;
  }>({ fingerprint, loading: Boolean(scope), results: {} });
  const current =
    state.fingerprint === fingerprint
      ? state
      : { fingerprint, loading: Boolean(scope), results: {} };

  useEffect(() => {
    const requestState = JSON.parse(fingerprint) as {
      scope?: ReportQueryScope;
      cardIds: string[];
      filters: Record<string, unknown>;
      interactionContext: Record<string, ReportInteractionContext>;
    };
    if (!requestState.scope || !requestState.cardIds.length) return;
    const controller = new AbortController();
    const input = {
      cardIds: requestState.cardIds,
      filters: requestState.filters,
      interactionContext: requestState.interactionContext,
    };
    const request =
      requestState.scope.kind === "draft"
        ? queryDraftReport(
            requestState.scope.reportId,
            input,
            controller.signal,
          )
        : queryPublishedReport(
            requestState.scope.reportId,
            requestState.scope.version,
            input,
            controller.signal,
          );
    void request
      .then((response) => {
        if (controller.signal.aborted) return;
        setState({
          fingerprint,
          loading: false,
          results: Object.fromEntries(
            response.results.map((result) => [result.cardId, result]),
          ),
        });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        setState({
          fingerprint,
          loading: false,
          results: {},
          error: error instanceof Error ? error.message : "报表批量查询失败",
        });
      });
    return () => controller.abort();
  }, [fingerprint]);
  return current;
}

export function executeReportInteraction(
  definition: ReportDefinition,
  event: CardInteractionEvent,
  updateContext: (cardId: string, value: ReportInteractionContext) => void,
  effects: { openModal?: (reportId: string) => void } = {},
) {
  const card = definition.cards.find((item) => item.id === event.cardId);
  const interaction = card?.interactions.find(
    (item) => item.event === event.event,
  );
  if (!card || !interaction) return;
  const action = interaction.action;
  const sourceDimensionId = card.binding.dimensions[0]?.id;
  const interactionValue =
    event.value ??
    (sourceDimensionId ? event.datum?.[sourceDimensionId] : undefined);
  if (
    (action.type === "crossFilter" || action.type === "drillDown") &&
    interactionValue !== undefined
  ) {
    updateContext(action.targetCardId || card.id, {
      sourceCardId: card.id,
      interactionId: interaction.id,
      value: interactionValue,
    });
    return;
  }
  if (action.type === "openModal" && action.targetReportId) {
    effects.openModal?.(action.targetReportId);
    return;
  }
  if (action.type === "navigate" && action.targetReportId) {
    window.location.assign(
      `/reports/${encodeURIComponent(action.targetReportId)}`,
    );
    return;
  }
  if (action.type === "openUrl" && action.url?.startsWith("/"))
    window.location.assign(action.url);
}
