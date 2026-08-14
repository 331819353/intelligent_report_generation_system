import assert from 'node:assert/strict'
import test from 'node:test'
import { prepareDataSourceChatInstruction } from './data-source-ai-input.ts'

test('moves a labelled password into secure input', () => {
  const result = prepareDataSourceChatInstruction('Oracle，密码: Secret2026!')
  assert.equal(result.password, 'Secret2026!')
  assert.equal(result.text, 'Oracle，密码: [已转入安全输入]')
})

test('moves a positional Oracle password into secure input', () => {
  const result = prepareDataSourceChatInstruction([
    '127.0.0.1: 1521/FREEPDB1',
    'REPORT_USER',
    'Secret2026!',
  ].join('\n'))
  assert.equal(result.password, 'Secret2026!')
  assert.equal(result.text, [
    '127.0.0.1: 1521/FREEPDB1',
    'REPORT_USER',
    '[已转入安全输入]',
  ].join('\n'))
})

test('moves a positional password when the endpoint uses full-width punctuation', () => {
  const result = prepareDataSourceChatInstruction([
    '127.0.0.1：1521/FREEPDB1',
    'TAKEOUT_USER',
    'TakeoutUser2026X',
  ].join('\n'))
  assert.equal(result.password, 'TakeoutUser2026X')
  assert.equal(result.text, [
    '127.0.0.1：1521/FREEPDB1',
    'TAKEOUT_USER',
    '[已转入安全输入]',
  ].join('\n'))
})

test('does not reinterpret ordinary multiline text as a password', () => {
  const input = ['127.0.0.1:1521/FREEPDB1', 'REPORT_USER', '用于经营分析'].join('\n')
  assert.deepEqual(prepareDataSourceChatInstruction(input), { text: input, password: '' })
})

test('moves a URI password into secure input', () => {
  const result = prepareDataSourceChatInstruction('postgresql://reporter:Secret2026!@db.internal:5432/analytics')
  assert.equal(result.password, 'Secret2026!')
  assert.equal(result.text, 'postgresql://reporter:[已转入安全输入]@db.internal:5432/analytics')
})
