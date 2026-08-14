export type PreparedDataSourceChatInstruction = {
  text: string
  password: string
}

const secureInputPlaceholder = '[已转入安全输入]'
const positionalEndpointPattern = /^[A-Za-z0-9.-]+\s*[:：]\s*[0-9]{1,5}\s*[/／]\s*[A-Za-z0-9_$#.-]+$/
const positionalUsernamePattern = /^[A-Za-z][A-Za-z0-9_$#.@-]{0,127}$/

const looksLikePassword = (value: string) => {
  if (value.length < 8 || value.length > 512 || /\s/.test(value)) return false
  const hasLetter = /[A-Za-z]/.test(value)
  const hasDigit = /[0-9]/.test(value)
  const hasSymbol = /[^A-Za-z0-9]/.test(value)
  return hasLetter && (hasDigit || hasSymbol)
}

/**
 * Removes credentials from conversational text before it is added to history or
 * sent to the configuration model. Besides labelled password fields, it handles
 * the common positional database block: host:port/database, username, password.
 */
export const prepareDataSourceChatInstruction = (input: string): PreparedDataSourceChatInstruction => {
  let detectedPassword = ''
  const hidePassword = (prefix: string, value: string, suffix: string) => {
    const candidate = value.trim()
    if (!detectedPassword && candidate && candidate !== secureInputPlaceholder) detectedPassword = candidate
    return `${prefix}${secureInputPlaceholder}${suffix}`
  }
  const markdownPassword = /(\|\s*(?:密码|口令|password|passwd)\s*\|\s*`?)([^`|\r\n]+)(`?\s*\|)/gi
  const inlinePassword = /((?:密码|口令|password|passwd)\s*[:=：]\s*["'`]?)([^"'`\r\n|]+)(["'`]?)/gi
  const uriPassword = /(\/\/[^/@\s:]+:)([^@/\s]+)(@)/gi
  const labelledRedacted = input
    .replace(markdownPassword, (_match, prefix: string, value: string, suffix: string) => hidePassword(prefix, value, suffix))
    .replace(inlinePassword, (_match, prefix: string, value: string, suffix: string) => hidePassword(prefix, value, suffix))
    .replace(uriPassword, (_match, prefix: string, value: string, suffix: string) => hidePassword(prefix, value, suffix))

  const lines = labelledRedacted.replace(/\r\n/g, '\n').split('\n')
  for (let endpointIndex = 0; endpointIndex < lines.length; endpointIndex += 1) {
    if (!positionalEndpointPattern.test(lines[endpointIndex].trim())) continue
    const followingIndexes: number[] = []
    for (let index = endpointIndex + 1; index < lines.length && followingIndexes.length < 2; index += 1) {
      if (lines[index].trim()) followingIndexes.push(index)
    }
    if (followingIndexes.length < 2) continue
    const username = lines[followingIndexes[0]].trim()
    const candidate = lines[followingIndexes[1]].trim()
    if (!positionalUsernamePattern.test(username) || !looksLikePassword(candidate)) continue
    if (!detectedPassword) detectedPassword = candidate
    lines[followingIndexes[1]] = secureInputPlaceholder
    break
  }

  return { text: lines.join('\n'), password: detectedPassword }
}
