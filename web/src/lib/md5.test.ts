import { expect, test } from 'vitest'
import { md5Hex } from './md5'

test('生成标准 UTF-8 MD5', () => {
  expect(md5Hex('')).toBe('d41d8cd98f00b204e9800998ecf8427e')
  expect(md5Hex('abc')).toBe('900150983cd24fb0d6963f7d28e17f72')
  expect(md5Hex('1234567890'.repeat(8))).toBe('57edf4a22be3c955ac49da2e2107b67a')
  expect(md5Hex('单行表头示例_csv')).toBe('08288c5f576b0c70d9b444a316b4ed8d')
})
