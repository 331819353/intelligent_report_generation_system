export type TemplateType = 'REPORT' | 'TABLE'
export type TemplateStatus = 'ACTIVE' | 'DRAFT' | 'OFFLINE'
export type AnalysisExplanationTemplateType = 'CONCLUSION_OUTPUT' | 'METRIC_DISPLAY' | 'ANALYSIS_OBJECTIVE' | 'DATA_SCOPE' | 'ANALYSIS_RULE' | 'DISPLAY_STANDARD' | 'BUSINESS_CONTEXT' | 'CUSTOM'
export type AnalysisContentCategory = 'OUTPUT' | 'ANALYSIS' | 'PRESENTATION' | 'CONTEXT' | 'CUSTOM'

export type ConclusionFormat = {
  referenceExample: string
  style: 'SUMMARY' | 'BULLETS' | 'NARRATIVE' | 'TABLE'
  instruction: string
  requiredFields: string[]
  maxLength: number
}

export type AnalysisChapterConfig = {
  analysisObjective: string
  scope: string
}

export type AnalysisSectionConfig = {
  keyQuestion: string
  analysisMethod: string
}

export type AnalysisMetricDisplay = {
  id: string
  metric: string
  role: 'CORE' | 'RESULT' | 'DRIVER' | 'COMPARISON'
  displayForm: 'LINE_CHART' | 'BAR_CHART' | 'PIE_CHART' | 'INDICATOR' | 'TABLE' | 'TEXT'
  displayRequirements: string
}

export type AnalysisItemConfig = {
  metricDisplays: AnalysisMetricDisplay[]
  comparisons: string[]
  dimensions: string[]
  filters: string[]
  breakdownRules: string[]
  displayRequirements: string
}

export type AnalysisExplanationItem = {
  id: string
  label: string
  content: string
}

export type AnalysisExplanationSection = {
  id: string
  templateType: AnalysisExplanationTemplateType
  title: string
  fields: Record<string, string>
  conclusionFormat?: ConclusionFormat
  itemConfig?: AnalysisItemConfig
  items: AnalysisExplanationItem[]
}

export type AnalysisNode = {
  id: string
  title: string
  description: string
  explanationSections: AnalysisExplanationSection[]
  children: AnalysisNode[]
}

export type AnalysisTemplate = {
  schemaVersion: '1.2'
  id: string
  code: string
  name: string
  templateType: TemplateType
  description: string
  status: TemplateStatus
  version: number
  owner: string
  updatedAt: string
  usageCount: number
  tags: string[]
  analysisTree: AnalysisNode[]
}

export const templateTypeLabels: Record<TemplateType, string> = {
  REPORT: '报告',
  TABLE: '报表',
}

export const conclusionStyleLabels: Record<ConclusionFormat['style'], string> = {
  SUMMARY: '摘要结论',
  BULLETS: '要点列表',
  NARRATIVE: '叙述段落',
  TABLE: '结论表格',
}

export const displayFormLabels: Record<AnalysisMetricDisplay['displayForm'], string> = {
  LINE_CHART: '趋势折线图',
  BAR_CHART: '对比柱状图',
  PIE_CHART: '结构占比图',
  INDICATOR: '核心指标卡',
  TABLE: '明细表格',
  TEXT: '文字分析',
}

export const metricRoleLabels: Record<AnalysisMetricDisplay['role'], string> = {
  CORE: '核心指标',
  RESULT: '结果指标',
  DRIVER: '动因指标',
  COMPARISON: '对比指标',
}

export type AnalysisExplanationFieldDefinition = {
  key: string
  label: string
  placeholder: string
  multiline?: boolean
  legacyLabels?: string[]
}

export const contentCategoryLabels: Record<AnalysisContentCategory, string> = {
  OUTPUT: '输出设计',
  ANALYSIS: '分析设计',
  PRESENTATION: '展示设计',
  CONTEXT: '业务约束',
  CUSTOM: '自定义',
}

export const explanationTemplateDefinitions: Record<AnalysisExplanationTemplateType, {
  category: AnalysisContentCategory
  title: string
  description: string
  fields: AnalysisExplanationFieldDefinition[]
  customItemLabels: string[]
}> = {
  CONCLUSION_OUTPUT: {
    category: 'OUTPUT',
    title: '结论输出',
    description: '配置结论样例、格式、字数和必含内容',
    fields: [],
    customItemLabels: [],
  },
  METRIC_DISPLAY: {
    category: 'OUTPUT',
    title: '指标展示',
    description: '逐项配置指标、角色、展示形式和分析条件',
    fields: [],
    customItemLabels: [],
  },
  ANALYSIS_OBJECTIVE: {
    category: 'ANALYSIS',
    title: '分析目标',
    description: '说明分析目的、覆盖范围和目标读者',
    fields: [
      { key: 'objective', label: '分析目的', placeholder: '本卡片希望回答什么经营问题？', multiline: true },
      { key: 'scope', label: '覆盖范围', placeholder: '涉及哪些业务、组织、区域或时间范围？', multiline: true },
      { key: 'audience', label: '目标读者', placeholder: '管理层、业务负责人或分析人员' },
      { key: 'successCriteria', label: '判断标准', placeholder: '怎样才算完成本项分析？', multiline: true },
    ],
    customItemLabels: [],
  },
  DATA_SCOPE: {
    category: 'ANALYSIS',
    title: '数据口径',
    description: '定义数据范围、统计周期、来源与业务口径',
    fields: [
      { key: 'dataRange', label: '数据范围', placeholder: '纳入和排除哪些数据？', multiline: true, legacyLabels: ['统计范围'] },
      { key: 'period', label: '统计周期', placeholder: '本期、近 12 个月或指定日期范围' },
      { key: 'definition', label: '口径定义', placeholder: '说明确认、计算和归属口径', multiline: true },
      { key: 'source', label: '数据来源', placeholder: '数据表、数据集或业务系统', multiline: true },
    ],
    customItemLabels: [],
  },
  ANALYSIS_RULE: {
    category: 'ANALYSIS',
    title: '分析规则',
    description: '约定计算、对比和异常识别逻辑',
    fields: [
      { key: 'question', label: '关键问题', placeholder: '这项分析要验证什么问题？', multiline: true },
      { key: 'calculation', label: '计算逻辑', placeholder: '说明计算、聚合或拆解方法', multiline: true },
      { key: 'baseline', label: '对比基准', placeholder: '目标、同期、环期或预测值' },
      { key: 'anomaly', label: '异常判定', placeholder: '阈值、偏差范围或识别规则', multiline: true },
    ],
    customItemLabels: [],
  },
  DISPLAY_STANDARD: {
    category: 'PRESENTATION',
    title: '展示规范',
    description: '约定布局、排序、标注和交互要求',
    fields: [
      { key: 'layout', label: '布局方式', placeholder: '单图、组合图、左右布局或表格' },
      { key: 'sorting', label: '排序规则', placeholder: '按金额、变化率或优先级排序' },
      { key: 'highlight', label: '重点标注', placeholder: '异常颜色、阈值线、TopN 或关键事件', multiline: true },
      { key: 'interaction', label: '交互要求', placeholder: '下钻、联动、筛选或悬浮提示', multiline: true },
    ],
    customItemLabels: [],
  },
  BUSINESS_CONTEXT: {
    category: 'CONTEXT',
    title: '业务补充',
    description: '补充业务背景、关注重点与行动约束',
    fields: [
      { key: 'background', label: '业务背景', placeholder: '补充影响判断的业务事件和上下文', multiline: true },
      { key: 'focus', label: '关注重点', placeholder: '管理者最关心的风险、机会或问题', multiline: true },
      { key: 'constraint', label: '行动约束', placeholder: '资源、时间、政策或组织限制', multiline: true },
      { key: 'owner', label: '责任角色', placeholder: '后续动作的责任部门或角色' },
    ],
    customItemLabels: [],
  },
  CUSTOM: {
    category: 'CUSTOM',
    title: '空白说明',
    description: '从一个空白说明项开始自由配置',
    fields: [],
    customItemLabels: ['说明标题'],
  },
}

export function inferExplanationTemplateType(title: string): AnalysisExplanationTemplateType {
  const match = (Object.entries(explanationTemplateDefinitions) as Array<[AnalysisExplanationTemplateType, typeof explanationTemplateDefinitions[AnalysisExplanationTemplateType]]>)
    .find(([type, definition]) => type !== 'CUSTOM' && definition.title === title.trim())
  return match?.[0] ?? 'CUSTOM'
}

export function defaultConclusionFormat(instruction = '概括关键变化、原因与建议动作。', referenceExample = ''): ConclusionFormat {
  return {
    referenceExample,
    style: 'BULLETS',
    instruction,
    requiredFields: ['核心发现', '原因判断', '行动建议'],
    maxLength: 300,
  }
}

export function defaultChapterConfig(): AnalysisChapterConfig {
  return { analysisObjective: '', scope: '' }
}

export function defaultSectionConfig(): AnalysisSectionConfig {
  return { keyQuestion: '', analysisMethod: '' }
}

export function defaultItemConfig(): AnalysisItemConfig {
  return {
    metricDisplays: [],
    comparisons: [],
    dimensions: [],
    filters: [],
    breakdownRules: [],
    displayRequirements: '展示本期值、同比、环比，并标注异常波动。',
  }
}

function normalizeExplanationSection(section: AnalysisExplanationSection): AnalysisExplanationSection {
  const type = section.templateType ?? inferExplanationTemplateType(section.title ?? '')
  const definition = explanationTemplateDefinitions[type]
  const legacyItems = section.items ?? []
  const existingFields = section.fields ?? {}
  const fields = Object.fromEntries(definition.fields.map(field => {
    const legacyLabels = [field.label, ...(field.legacyLabels ?? [])]
    const legacyValue = legacyItems.find(item => legacyLabels.includes(item.label))?.content ?? ''
    return [field.key, existingFields[field.key] ?? legacyValue]
  }))
  const metricConfig = section.itemConfig
  return {
    ...section,
    templateType: type,
    fields,
    conclusionFormat: type === 'CONCLUSION_OUTPUT'
      ? { ...defaultConclusionFormat(), ...(section.conclusionFormat ?? {}) }
      : undefined,
    itemConfig: type === 'METRIC_DISPLAY'
      ? {
        ...defaultItemConfig(),
        ...(metricConfig ?? {}),
        metricDisplays: (metricConfig?.metricDisplays ?? []).map((metric, index) => ({
          ...metric,
          id: metric.id || `${section.id}-metric-${index + 1}`,
        })),
        comparisons: [...(metricConfig?.comparisons ?? [])],
        dimensions: [...(metricConfig?.dimensions ?? [])],
        filters: [...(metricConfig?.filters ?? [])],
        breakdownRules: [...(metricConfig?.breakdownRules ?? [])],
      }
      : undefined,
    items: legacyItems.map(item => ({ ...item })),
  }
}

export function createExplanationSection(
  templateType: AnalysisExplanationTemplateType,
  id: string,
  title = explanationTemplateDefinitions[templateType].title,
): AnalysisExplanationSection {
  const definition = explanationTemplateDefinitions[templateType]
  return {
    id,
    templateType,
    title,
    fields: Object.fromEntries(definition.fields.map(field => [field.key, ''])),
    conclusionFormat: templateType === 'CONCLUSION_OUTPUT' ? defaultConclusionFormat() : undefined,
    itemConfig: templateType === 'METRIC_DISPLAY' ? defaultItemConfig() : undefined,
    items: definition.customItemLabels.map((label, index) => ({ id: `${id}-item-${index + 1}`, label, content: '' })),
  }
}

export function createAnalysisNode(id: string, title = '新建节点'): AnalysisNode {
  return {
    id,
    title,
    description: '',
    explanationSections: [],
    children: [],
  }
}

type LegacyAnalysisNodeType = 'TOPIC' | 'CHAPTER' | 'SECTION' | 'ITEM'
type LegacyAnalysisItemConfig = Partial<AnalysisItemConfig> & {
    metrics?: string[]
    displayForm?: AnalysisMetricDisplay['displayForm']
    comparisonDimensions?: string[]
}
type LegacyAnalysisNode = Partial<AnalysisNode> & {
  id: string
  type?: LegacyAnalysisNodeType
  conclusionFormat?: ConclusionFormat
  chapterConfig?: AnalysisChapterConfig
  sectionConfig?: AnalysisSectionConfig
  itemConfig?: LegacyAnalysisItemConfig
  children?: LegacyAnalysisNode[]
}

function normalizeLegacyItemConfig(nodeID: string, legacyItem?: LegacyAnalysisItemConfig): AnalysisItemConfig {
  const metricDisplays = legacyItem?.metricDisplays?.map((metric, index) => ({
    id: metric.id || `${nodeID}-metric-${index + 1}`,
    metric: metric.metric ?? '',
    role: metric.role ?? 'CORE',
    displayForm: metric.displayForm ?? 'LINE_CHART',
    displayRequirements: metric.displayRequirements ?? '',
  })) ?? legacyItem?.metrics?.map((metric, index) => ({
    id: `${nodeID}-metric-${index + 1}`,
    metric,
    role: 'CORE' as const,
    displayForm: legacyItem.displayForm ?? 'LINE_CHART',
    displayRequirements: '',
  })) ?? []
  return {
    ...defaultItemConfig(),
    displayRequirements: legacyItem?.displayRequirements ?? defaultItemConfig().displayRequirements,
    metricDisplays,
    comparisons: legacyItem?.comparisons ?? legacyItem?.comparisonDimensions ?? [],
    dimensions: legacyItem?.dimensions ?? [],
    filters: legacyItem?.filters ?? [],
    breakdownRules: legacyItem?.breakdownRules ?? [],
  }
}

/** 将旧版按主题/章节/小节/具体项划分的配置迁移为无类型节点与通用内容卡片。 */
export function normalizeAnalysisNode(node: AnalysisNode | LegacyAnalysisNode): AnalysisNode {
  const legacyNode = node as LegacyAnalysisNode
  const existingSections = (node.explanationSections ?? []).map(normalizeExplanationSection)
  const migratedSections: AnalysisExplanationSection[] = []
  if (legacyNode.chapterConfig) {
    const section = createExplanationSection('ANALYSIS_OBJECTIVE', `${node.id}-legacy-objective`)
    section.fields = {
      ...section.fields,
      objective: legacyNode.chapterConfig.analysisObjective || node.description || '',
      scope: legacyNode.chapterConfig.scope ?? '',
    }
    migratedSections.push(section)
  }
  if (legacyNode.sectionConfig) {
    const section = createExplanationSection('ANALYSIS_RULE', `${node.id}-legacy-rule`)
    section.fields = {
      ...section.fields,
      question: legacyNode.sectionConfig.keyQuestion || node.description || '',
      calculation: legacyNode.sectionConfig.analysisMethod ?? '',
    }
    migratedSections.push(section)
  }
  if (legacyNode.itemConfig) {
    const section = createExplanationSection('METRIC_DISPLAY', `${node.id}-legacy-metrics`)
    section.itemConfig = normalizeLegacyItemConfig(node.id, legacyNode.itemConfig)
    migratedSections.push(section)
  }
  if (legacyNode.conclusionFormat) {
    const section = createExplanationSection('CONCLUSION_OUTPUT', `${node.id}-legacy-conclusion`)
    section.conclusionFormat = { ...defaultConclusionFormat(), ...legacyNode.conclusionFormat }
    migratedSections.push(section)
  }
  return {
    id: node.id,
    title: node.title ?? '未命名节点',
    description: node.description ?? '',
    explanationSections: [...migratedSections, ...existingSections],
    children: (node.children ?? []).map(normalizeAnalysisNode),
  }
}

export function normalizeAnalysisTemplate(template: AnalysisTemplate): AnalysisTemplate {
  return { ...template, schemaVersion: '1.2', analysisTree: (template.analysisTree ?? []).map(normalizeAnalysisNode) }
}

export function countAnalysisNodes(nodes: AnalysisNode[]): number {
  return nodes.reduce((total, node) => total + 1 + countAnalysisNodes(node.children), 0)
}

export function findAnalysisNode(nodes: AnalysisNode[], id: string): AnalysisNode | undefined {
  for (const node of nodes) {
    if (node.id === id) return node
    const child = findAnalysisNode(node.children, id)
    if (child) return child
  }
  return undefined
}

export function findAnalysisNodePath(nodes: AnalysisNode[], id: string, parents: AnalysisNode[] = []): AnalysisNode[] {
  for (const node of nodes) {
    const path = [...parents, node]
    if (node.id === id) return path
    const childPath = findAnalysisNodePath(node.children, id, path)
    if (childPath.length > 0) return childPath
  }
  return []
}

export function updateAnalysisNode(nodes: AnalysisNode[], id: string, update: (node: AnalysisNode) => AnalysisNode): AnalysisNode[] {
  return nodes.map(node => {
    if (node.id === id) return update(node)
    const children = updateAnalysisNode(node.children, id, update)
    return children === node.children ? node : { ...node, children }
  })
}

export function appendAnalysisNode(nodes: AnalysisNode[], parentId: string | null, child: AnalysisNode): AnalysisNode[] {
  if (!parentId) return [...nodes, child]
  return updateAnalysisNode(nodes, parentId, node => ({ ...node, children: [...node.children, child] }))
}

export function removeAnalysisNode(nodes: AnalysisNode[], id: string): AnalysisNode[] {
  return nodes
    .filter(node => node.id !== id)
    .map(node => ({ ...node, children: removeAnalysisNode(node.children, id) }))
}

export function createTemplateSkeleton(input: {
  id: string
  name: string
  code: string
  description: string
  templateType: TemplateType
  now: string
}): AnalysisTemplate {
  const rootTitle = input.name.startsWith('未命名')
    ? input.templateType === 'REPORT' ? '这份报告要回答什么核心问题？' : '这张报表要监控什么核心问题？'
    : input.name.replace(/模板$/, '') || '要回答什么核心问题？'
  const root = createAnalysisNode(`${input.id}-root`, rootTitle)
  root.explanationSections = [createExplanationSection('CONCLUSION_OUTPUT', `${root.id}-conclusion`)]
  return {
    schemaVersion: '1.2',
    id: input.id,
    code: input.code,
    name: input.name,
    templateType: input.templateType,
    description: input.description,
    status: 'DRAFT',
    version: 1,
    owner: '王敏',
    updatedAt: input.now,
    usageCount: 0,
    tags: input.templateType === 'REPORT' ? ['经营分析', '周期报告'] : ['指标监控', '业务报表'],
    analysisTree: [root],
  }
}
