import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  semanticGovernanceAPI,
  type DimensionSurveyCandidate,
} from './semantic-governance'

afterEach(() => vi.restoreAllMocks())

const candidate = (): DimensionSurveyCandidate => ({
  id: 'candidate/id',
  surveyRunId: 'survey-1',
  datasetId: 'dataset-1',
  datasetVersionId: 'version-1',
  schemaHash: 'a'.repeat(64),
  materializationId: 'materialization-1',
  materializationSnapshotHash: 'b'.repeat(64),
  materializationRowCount: 120,
  fieldId: 'field-1',
  fieldCode: 'ecosystem',
  fieldRole: 'DIMENSION',
  canonicalType: 'STRING',
  semanticType: 'CATEGORY',
  riskHighCardinality: false,
  riskSensitive: false,
  evidence: {},
  proposedCode: 'ecosystem',
  proposedName: '生态圈',
  proposedDescription: '生态圈维度',
  proposedDimensionType: 'ORGANIZATION',
  proposedMemberIndexPolicy: 'FULL',
  proposedHighCardinality: false,
  proposedSensitive: false,
  status: 'SUGGESTED',
  version: 3,
  generatedBy: 'system',
  updatedBy: 'system',
  createdAt: '2026-07-24T00:00:00Z',
  updatedAt: '2026-07-24T00:00:00Z',
  profile: {
    id: 'profile-1',
    status: 'SUCCEEDED',
    profileVersion: 'dws-dimension-profile-v1',
    policyVersion: 'dimension-member-policy-v1',
    materializationId: 'materialization-1',
    materializationSnapshotHash: 'b'.repeat(64),
    expectedRowCount: 120,
    rowCount: 120,
    nonNullCount: 120,
    nullCount: 0,
    distinctCount: 4,
    distinctOverflow: false,
    distinctCap: 100000,
    distinctRatio: 4 / 120,
    riskHighCardinality: false,
    recommendedMemberIndexPolicy: 'FULL',
    attempt: 1,
    maxAttempts: 3,
    createdAt: '2026-07-24T00:00:00Z',
    updatedAt: '2026-07-24T00:00:00Z',
    completedAt: '2026-07-24T00:00:00Z',
  },
})

describe('semanticGovernanceAPI', () => {
  test('uses the governed candidate filters and optimistic-lock mutation bodies', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      items: [candidate()],
      total: 1,
      limit: 40,
      offset: 5,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))

    await semanticGovernanceAPI.listCandidates({
      status: 'SUGGESTED',
      fieldRole: 'DIMENSION',
      datasetId: 'dataset/id',
      datasetVersionId: 'version/id',
      limit: 40,
      offset: 5,
    })
    expect(fetchMock.mock.calls[0][0]).toBe(
      '/api/v1/semantic/dimension-survey-candidates?status=SUGGESTED&fieldRole=DIMENSION&datasetId=dataset%2Fid&datasetVersionId=version%2Fid&limit=40&offset=5',
    )
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ cache: 'no-store' })

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(candidate()), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    await semanticGovernanceAPI.updateCandidate('candidate/id', {
      expectedVersion: 3,
      code: 'ecosystem',
      name: '智家生态圈',
      description: '组织维度',
      dimensionType: 'ORGANIZATION',
      memberIndexPolicy: 'EXACT_ONLY',
      highCardinality: false,
      sensitive: true,
    })
    const [updateURL, updateInit] = fetchMock.mock.calls[1]
    expect(updateURL).toBe('/api/v1/semantic/dimension-survey-candidates/candidate%2Fid')
    expect(updateInit?.method).toBe('PUT')
    expect(JSON.parse(String(updateInit?.body))).toEqual({
      expectedVersion: 3,
      code: 'ecosystem',
      name: '智家生态圈',
      description: '组织维度',
      dimensionType: 'ORGANIZATION',
      memberIndexPolicy: 'EXACT_ONLY',
      highCardinality: false,
      sensitive: true,
    })

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({}), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    await semanticGovernanceAPI.acceptCandidate('candidate/id', 4)
    expect(fetchMock.mock.calls[2][0]).toBe('/api/v1/semantic/dimension-survey-candidates/candidate%2Fid/accept')
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toEqual({ expectedVersion: 4 })

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(candidate()), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    await semanticGovernanceAPI.rejectCandidate('candidate/id', 5, '不是稳定分析维度')
    expect(fetchMock.mock.calls[3][0]).toBe('/api/v1/semantic/dimension-survey-candidates/candidate%2Fid/reject')
    expect(JSON.parse(String(fetchMock.mock.calls[3][1]?.body))).toEqual({
      expectedVersion: 5,
      reason: '不是稳定分析维度',
    })
  })

  test('submits member refresh, historical alias and compatibility verification to exact semantic endpoints', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({}), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await semanticGovernanceAPI.createRefreshJob('dimension/id', 7, 'refresh-key')
    let [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/semantic/dimensions/dimension%2Fid/member-refresh-jobs')
    expect(init?.headers).toMatchObject({ 'Idempotency-Key': 'refresh-key' })
    expect(JSON.parse(String(init?.body))).toEqual({ expectedDimensionVersion: 7 })

    await semanticGovernanceAPI.createAlias({
      dimensionId: 'dimension-1',
      dimensionMemberId: 'member-1',
      alias: '690',
      aliasType: 'LEGACY',
    })
    ;[url, init] = fetchMock.mock.calls[1]
    expect(url).toBe('/api/v1/semantic/dimension-member-aliases')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual({
      dimensionId: 'dimension-1',
      dimensionMemberId: 'member-1',
      alias: '690',
      aliasType: 'LEGACY',
    })

    await semanticGovernanceAPI.verifyCompatibility('compatibility/id', 2)
    ;[url, init] = fetchMock.mock.calls[2]
    expect(url).toBe('/api/v1/semantic/dimension-metric-compatibilities/compatibility%2Fid/verify')
    expect(JSON.parse(String(init?.body))).toEqual({ expectedVersion: 2 })

    await semanticGovernanceAPI.evaluatePermission('MANAGE')
    ;[url, init] = fetchMock.mock.calls[3]
    expect(url).toBe('/api/v1/permissions/evaluate')
    expect(JSON.parse(String(init?.body))).toEqual({
      resourceType: 'DATASET',
      action: 'MANAGE',
      objectId: '',
    })
  })
})
