import Ajv2020, { type ErrorObject } from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import reportSchema from '../../../api/schemas/report-json-v1.schema.json'
import type { ReportDocument, ReportValidationIssue } from './report-contract'

const ajv = new Ajv2020({ allErrors: true, strict: true })
addFormats(ajv)
const validateSchema = ajv.compile<ReportDocument>(reportSchema)

export type ReportValidationResult = {
  document?: ReportDocument
  errors: ReportValidationIssue[]
  warnings: ReportValidationIssue[]
}

/**
 * 使用与 Go 服务端相同的正式 Schema 校验报告。
 * 未知组件类型可由注册表降级，其余结构错误必须阻止整页渲染。
 */
export function validateReportDocument(source: unknown): ReportValidationResult {
  if (validateSchema(source)) {
    const semanticErrors = [
      ...validateStickyContainerReferences(source),
      ...validateDesignerBlockSemantics(source),
    ]
    return { document: semanticErrors.length === 0 ? source : undefined, errors: semanticErrors, warnings: [] }
  }
  const errors = validateSchema.errors ?? []
  const unknownComponents = errors.filter(isUnknownComponentType)
  const fatalErrors = errors.filter(error => !isUnknownComponentType(error))
  const structuralErrors = fatalErrors.map(formatIssue)
  const semanticErrors = fatalErrors.length === 0
    ? [
        ...validateStickyContainerReferences(source as ReportDocument),
        ...validateDesignerBlockSemantics(source as ReportDocument),
      ]
    : []
  return {
    document: structuralErrors.length === 0 && semanticErrors.length === 0 ? source as ReportDocument : undefined,
    errors: [...structuralErrors, ...semanticErrors],
    warnings: unknownComponents.map(formatIssue),
  }
}

/** 菜单唯一性、固定占位和内容区引用属于跨对象约束，由语义层校验。 */
function validateDesignerBlockSemantics(document: ReportDocument): ReportValidationIssue[] {
  const issues: ReportValidationIssue[] = []
  document.pages.forEach((page, pageIndex) => {
    const menus = page.blocks
      .map((block, blockIndex) => ({ block, blockIndex }))
      .filter(item => item.block.kind === 'MENU')
    if (menus.length > 1) {
      issues.push({ path: `pages[${pageIndex}].blocks`, reason: '每个页面只能有一个菜单区' })
    }
    menus.forEach(({ block, blockIndex }) => {
      const path = `pages[${pageIndex}].blocks[${blockIndex}]`
      if (block.grid.x !== 0 || block.grid.y !== 0 || block.grid.w !== 12 || block.grid.h !== 2) {
        issues.push({ path: `${path}.grid`, reason: '菜单区必须占据页面顶部 12×2 分格' })
      }
      if (!block.menuLayout) issues.push({ path: `${path}.menuLayout`, reason: '菜单区必须声明四宫格布局' })
    })
    page.blocks.forEach((block, blockIndex) => {
      if (block.kind !== 'CONTENT') return
      const path = `pages[${pageIndex}].blocks[${blockIndex}]`
      if (!block.contentLayout) {
        issues.push({ path: `${path}.contentLayout`, reason: '内容区必须声明标题、结论和组件图区域' })
        return
      }
      const componentIDs = new Set(block.components.map(component => component.id))
      Object.entries(block.contentLayout.areas).forEach(([areaName, area]) => {
        area.componentIds.forEach((componentID, componentIndex) => {
          if (!componentIDs.has(componentID)) {
            issues.push({
              path: `${path}.contentLayout.areas.${areaName}.componentIds[${componentIndex}]`,
              reason: '必须引用所属内容区内的组件',
            })
          }
        })
      })
    })
  })
  return issues
}

/** Schema 无法表达跨对象祖先关系，因此在渲染入口补一次确定性的引用校验。 */
function validateStickyContainerReferences(document: ReportDocument): ReportValidationIssue[] {
  const issues: ReportValidationIssue[] = []
  document.pages.forEach((page, pageIndex) => {
    page.blocks.forEach((block, blockIndex) => {
      const blockPath = `pages[${pageIndex}].blocks[${blockIndex}]`
      if (block.sticky.enabled && block.sticky.scope === 'CONTAINER' && block.sticky.containerId !== page.id) {
        issues.push({ path: `${blockPath}.sticky.containerId`, reason: '必须引用所属页面祖先' })
      }
      block.components.forEach((component, componentIndex) => {
        if (!component.sticky.enabled || component.sticky.scope !== 'CONTAINER') return
        const path = `${blockPath}.components[${componentIndex}].sticky.containerId`
        const matches = Number(component.sticky.containerId === page.id) + Number(component.sticky.containerId === block.id)
        if (matches === 0) issues.push({ path, reason: '必须引用所属页面或分块祖先' })
        else if (matches > 1) issues.push({ path, reason: '同时匹配多个祖先类型，容器引用存在歧义' })
      })
    })
  })
  return issues
}

function isUnknownComponentType(error: ErrorObject): boolean {
  return error.keyword === 'enum' && /^\/pages\/\d+\/blocks\/\d+\/components\/\d+\/type$/.test(error.instancePath)
}

function formatIssue(error: ErrorObject): ReportValidationIssue {
  const missing = error.keyword === 'required' && typeof error.params.missingProperty === 'string'
    ? `/${error.params.missingProperty}`
    : ''
  return {
    path: pointerToPath(`${error.instancePath}${missing}`),
    reason: translateReason(error),
  }
}

function pointerToPath(pointer: string): string {
  if (!pointer) return '$'
  return pointer
    .split('/')
    .slice(1)
    .map(segment => /^\d+$/.test(segment) ? `[${segment}]` : segment.replaceAll('~1', '/').replaceAll('~0', '~'))
    .reduce((path, segment) => segment.startsWith('[') ? `${path}${segment}` : path ? `${path}.${segment}` : segment, '')
}

function translateReason(error: ErrorObject): string {
  switch (error.keyword) {
    case 'required': return '缺少必填字段'
    case 'additionalProperties': return `包含未知字段 ${String(error.params.additionalProperty)}`
    case 'enum': return '不在允许的枚举范围内'
    case 'const': return `必须为 ${JSON.stringify(error.params.allowedValue)}`
    case 'minimum': return `不能小于 ${String(error.params.limit)}`
    case 'minItems': return `至少需要 ${String(error.params.limit)} 项`
    case 'minLength': return '不能为空'
    case 'maxLength': return `长度不能超过 ${String(error.params.limit)}`
    case 'pattern': return '格式不符合合同要求'
    case 'format': return `必须符合 ${String(error.params.format)} 格式`
    case 'type': return `类型必须为 ${String(error.params.type)}`
    default: return error.message ? `合同校验失败：${error.message}` : '合同校验失败'
  }
}
