import type { BusinessDomain } from './administration'

const currentDomainKey = 'intelligent-report-current-domain'
export const domainChangedEvent = 'business-domain-changed'
export const domainCatalogChangedEvent = 'business-domain-catalog-changed'

/** 读取当前标签页选中的业务领域。 */
export function currentDomain(): BusinessDomain | null {
  const value = sessionStorage.getItem(currentDomainKey)
  if (!value) return null
  try {
    const domain = JSON.parse(value) as BusinessDomain
    return domain?.id && domain?.name ? domain : null
  } catch {
    return null
  }
}

/** 为依赖领域身份的控件提供稳定的外部状态快照。 */
export function currentDomainID() {
  return currentDomain()?.id || ''
}

/** 订阅当前领域切换；用于立即撤销跨领域编辑能力。 */
export function subscribeDomainChange(listener: () => void) {
  window.addEventListener(domainChangedEvent, listener)
  return () => window.removeEventListener(domainChangedEvent, listener)
}

/** 保存业务领域上下文并通知当前页面中的其他组件。 */
export function selectDomain(domain: BusinessDomain) {
  sessionStorage.setItem(currentDomainKey, JSON.stringify(domain))
  window.dispatchEvent(new CustomEvent(domainChangedEvent, { detail: domain }))
}

/** 领域创建或状态变化后通知侧栏重新读取可用领域。 */
export function notifyDomainCatalogChanged() {
  window.dispatchEvent(new Event(domainCatalogChangedEvent))
}

/** 退出登录时清理领域上下文，避免下一个账号继承前一个账号的选择。 */
export function clearDomain() {
  sessionStorage.removeItem(currentDomainKey)
}
