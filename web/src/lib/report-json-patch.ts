export type ReportJSONPatchOperation =
  | { op: 'add'; path: string; value: unknown }
  | { op: 'remove'; path: string }
  | { op: 'replace'; path: string; value: unknown }

/** API 合同复用同一 Patch 联合，避免历史模块产生第二套操作类型。 */
export type JsonPatchOperation = ReportJSONPatchOperation
export type ReportJSONPatch = ReportJSONPatchOperation[]
export const MAX_REPORT_JSON_PATCH_OPERATIONS = 100

export type ReportJSONPatchPair = {
  forward: ReportJSONPatch
  inverse: ReportJSONPatch
}

/** JSON Patch 合同错误，path 指向补丁自身或目标文档中的失败位置。 */
export class ReportJSONPatchError extends Error {
  constructor(public readonly path: string, message: string) {
    super(message)
    this.name = 'ReportJSONPatchError'
  }
}

/**
 * 生成可往返的 RFC 6902 子集补丁。数组优先保留公共首尾，避免组件删除时把
 * 后续全部元素误记成替换；正向与逆向补丁都只包含 add/remove/replace。
 */
export function createReportJSONPatch(before: unknown, after: unknown): ReportJSONPatchPair {
  const safeBefore = cloneJSON(before, '$')
  const safeAfter = cloneJSON(after, '$')
  return {
    forward: diffJSON(safeBefore, safeAfter, ''),
    inverse: diffJSON(safeAfter, safeBefore, ''),
  }
}

/** 在深拷贝上原子应用补丁；任一操作失败时，调用方传入的文档保持不变。 */
export function applyReportJSONPatch<T>(document: T, patch: readonly ReportJSONPatchOperation[]): T {
  let result = cloneJSON(document, '$')
  if (!Array.isArray(patch)) throw new ReportJSONPatchError('$', 'JSON Patch 必须为数组')
  for (let index = 0; index < patch.length; index += 1) {
    const operation = validateOperation(patch[index], index)
    const tokens = parsePointer(operation.path, `$[${index}].path`)
    if (tokens.length === 0) {
      if (operation.op === 'remove') throw new ReportJSONPatchError(operation.path, '不允许删除报告文档根节点')
      result = cloneJSON(operation.value, `$[${index}].value`)
      continue
    }
    const { parent, token } = resolveParent(result, tokens, operation.path)
    applyOperation(parent, token, operation, index)
  }
  return result as T
}

function diffJSON(before: unknown, after: unknown, path: string): ReportJSONPatch {
  if (jsonEqual(before, after)) return []
  const operations = Array.isArray(before) && Array.isArray(after)
    ? diffArray(before, after, path)
    : isJSONObject(before) && isJSONObject(after)
      ? diffObject(before, after, path)
      : [{ op: 'replace' as const, path, value: cloneJSON(after, path || '$') }]
  // 服务端单请求最多接受 100 个原子操作；过细时替换最近的非根子树，仍由服务端
  // 根据前后文档和稳定实体 target 重新校验真实语义，不能借此扩大修改范围。
  if (path !== '' && operations.length > MAX_REPORT_JSON_PATCH_OPERATIONS) {
    return [{ op: 'replace', path, value: cloneJSON(after, path) }]
  }
  return operations
}

function diffObject(before: Record<string, unknown>, after: Record<string, unknown>, path: string): ReportJSONPatch {
  const operations: ReportJSONPatch = []
  const beforeKeys = Object.keys(before).sort()
  const afterKeys = Object.keys(after).sort()
  const afterSet = new Set(afterKeys)
  const beforeSet = new Set(beforeKeys)
  for (const key of beforeKeys) {
    if (!afterSet.has(key)) operations.push({ op: 'remove', path: joinPointer(path, key) })
  }
  for (const key of afterKeys) {
    const childPath = joinPointer(path, key)
    if (!beforeSet.has(key)) operations.push({ op: 'add', path: childPath, value: cloneJSON(after[key], childPath) })
    else operations.push(...diffJSON(before[key], after[key], childPath))
  }
  return operations
}

function diffArray(before: unknown[], after: unknown[], path: string): ReportJSONPatch {
  let prefix = 0
  while (prefix < before.length && prefix < after.length && jsonEqual(before[prefix], after[prefix])) prefix += 1
  let suffix = 0
  while (
    suffix < before.length - prefix
    && suffix < after.length - prefix
    && jsonEqual(before[before.length - 1 - suffix], after[after.length - 1 - suffix])
  ) suffix += 1

  const beforeMiddleLength = before.length - prefix - suffix
  const afterMiddleLength = after.length - prefix - suffix
  if (beforeMiddleLength === afterMiddleLength) {
    return Array.from({ length: beforeMiddleLength }, (_, offset) =>
      diffJSON(before[prefix + offset], after[prefix + offset], joinPointer(path, String(prefix + offset))),
    ).flat()
  }

  const operations: ReportJSONPatch = []
  // 删除必须从尾部开始，避免较小下标在前序删除后指向错误元素。
  for (let offset = beforeMiddleLength - 1; offset >= 0; offset -= 1) {
    operations.push({ op: 'remove', path: joinPointer(path, String(prefix + offset)) })
  }
  for (let offset = 0; offset < afterMiddleLength; offset += 1) {
    operations.push({ op: 'add', path: joinPointer(path, String(prefix + offset)), value: cloneJSON(after[prefix + offset], joinPointer(path, String(prefix + offset))) })
  }
  return operations
}

function validateOperation(value: unknown, index: number): ReportJSONPatchOperation {
  const basePath = `$[${index}]`
  if (!isJSONObject(value)) throw new ReportJSONPatchError(basePath, 'JSON Patch 操作必须为对象')
  const op = value.op
  const path = value.path
  if (op !== 'add' && op !== 'remove' && op !== 'replace') throw new ReportJSONPatchError(`${basePath}.op`, '仅支持 add、remove、replace')
  if (typeof path !== 'string') throw new ReportJSONPatchError(`${basePath}.path`, 'JSON Pointer 必须为字符串')
  const allowed = op === 'remove' ? new Set(['op', 'path']) : new Set(['op', 'path', 'value'])
  const unknown = Object.keys(value).find(key => !allowed.has(key))
  if (unknown) throw new ReportJSONPatchError(`${basePath}.${unknown}`, 'JSON Patch 操作包含未知字段')
  if (op === 'remove') {
    if (Object.hasOwn(value, 'value')) throw new ReportJSONPatchError(`${basePath}.value`, 'remove 操作不能携带 value')
    return { op, path }
  }
  if (!Object.hasOwn(value, 'value')) throw new ReportJSONPatchError(`${basePath}.value`, `${op} 操作必须携带 value`)
  return { op, path, value: cloneJSON(value.value, `${basePath}.value`) }
}

function parsePointer(pointer: string, errorPath: string): string[] {
  if (pointer === '') return []
  if (!pointer.startsWith('/')) throw new ReportJSONPatchError(errorPath, 'JSON Pointer 必须为空或以 / 开头')
  return pointer.slice(1).split('/').map(raw => {
    if (/~(?:[^01]|$)/.test(raw)) throw new ReportJSONPatchError(errorPath, 'JSON Pointer 包含非法转义')
    return raw.replace(/~1/g, '/').replace(/~0/g, '~')
  })
}

function resolveParent(root: unknown, tokens: string[], path: string): { parent: Record<string, unknown> | unknown[]; token: string } {
  let current = root
  for (const token of tokens.slice(0, -1)) {
    if (Array.isArray(current)) {
      const index = parseArrayIndex(token, current.length, false, path)
      current = current[index]
    } else if (isJSONObject(current)) {
      if (!Object.hasOwn(current, token)) throw new ReportJSONPatchError(path, `父路径 ${token} 不存在`)
      current = current[token]
    } else {
      throw new ReportJSONPatchError(path, 'JSON Pointer 的父路径不是对象或数组')
    }
  }
  if (!Array.isArray(current) && !isJSONObject(current)) throw new ReportJSONPatchError(path, 'JSON Pointer 的目标父节点不是对象或数组')
  return { parent: current, token: tokens.at(-1)! }
}

function applyOperation(parent: Record<string, unknown> | unknown[], token: string, operation: ReportJSONPatchOperation, operationIndex: number) {
  const valuePath = `$[${operationIndex}].value`
  if (Array.isArray(parent)) {
    if (operation.op === 'add') {
      const index = token === '-' ? parent.length : parseArrayIndex(token, parent.length, true, operation.path)
      parent.splice(index, 0, cloneJSON(operation.value, valuePath))
      return
    }
    const index = parseArrayIndex(token, parent.length, false, operation.path)
    if (operation.op === 'remove') parent.splice(index, 1)
    else parent[index] = cloneJSON(operation.value, valuePath)
    return
  }

  if (operation.op !== 'add' && !Object.hasOwn(parent, token)) throw new ReportJSONPatchError(operation.path, '目标属性不存在')
  if (operation.op === 'remove') {
    delete parent[token]
    return
  }
  // defineProperty 避免 __proto__ 等合法 JSON 键改变对象原型。
  Object.defineProperty(parent, token, { value: cloneJSON(operation.value, valuePath), enumerable: true, configurable: true, writable: true })
}

function parseArrayIndex(token: string, length: number, allowEnd: boolean, path: string): number {
  if (!/^(0|[1-9]\d*)$/.test(token)) throw new ReportJSONPatchError(path, '数组下标必须为无前导零的非负整数')
  const index = Number(token)
  const maximum = allowEnd ? length : length - 1
  if (!Number.isSafeInteger(index) || index > maximum) throw new ReportJSONPatchError(path, '数组下标越界')
  return index
}

function joinPointer(parent: string, token: string): string {
  return `${parent}/${token.replace(/~/g, '~0').replace(/\//g, '~1')}`
}

function jsonEqual(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true
  if (Array.isArray(left) && Array.isArray(right)) {
    return left.length === right.length && left.every((value, index) => jsonEqual(value, right[index]))
  }
  if (isJSONObject(left) && isJSONObject(right)) {
    const leftKeys = Object.keys(left).sort()
    const rightKeys = Object.keys(right).sort()
    return leftKeys.length === rightKeys.length
      && leftKeys.every((key, index) => key === rightKeys[index] && jsonEqual(left[key], right[key]))
  }
  return false
}

function cloneJSON(value: unknown, path: string, seen = new Set<object>()): unknown {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new ReportJSONPatchError(path, 'JSON 数字必须为有限值')
    return value
  }
  if (typeof value !== 'object') throw new ReportJSONPatchError(path, '值不是有效 JSON 类型')
  if (seen.has(value)) throw new ReportJSONPatchError(path, 'JSON 值不能包含循环引用')
  seen.add(value)
  try {
    if (Array.isArray(value)) return value.map((item, index) => cloneJSON(item, `${path}[${index}]`, seen))
    if (!isJSONObject(value)) throw new ReportJSONPatchError(path, 'JSON 对象必须为普通对象')
    const clone: Record<string, unknown> = {}
    for (const key of Object.keys(value)) {
      Object.defineProperty(clone, key, { value: cloneJSON(value[key], `${path}.${key}`, seen), enumerable: true, configurable: true, writable: true })
    }
    return clone
  } finally {
    seen.delete(value)
  }
}

function isJSONObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return false
  const prototype = Object.getPrototypeOf(value)
  return prototype === Object.prototype || prototype === null
}
