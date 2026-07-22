const shifts = [
  7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
  5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
  4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
  6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
]

const constants = Array.from({ length: 64 }, (_, index) => Math.floor(Math.abs(Math.sin(index + 1)) * 0x100000000) >>> 0)
const rotateLeft = (value: number, shift: number) => ((value << shift) | (value >>> (32 - shift))) >>> 0
const littleEndianHex = (value: number) => [0, 8, 16, 24]
  .map(shift => ((value >>> shift) & 0xff).toString(16).padStart(2, '0'))
  .join('')

/** 生成 UTF-8 文本的标准小写 MD5；仅用于稳定业务编码，不用于密码或签名。 */
export const md5Hex = (value: string) => {
  const input = new TextEncoder().encode(value)
  const zeroPadding = (56 - ((input.length + 1) % 64) + 64) % 64
  const message = new Uint8Array(input.length + 1 + zeroPadding + 8)
  message.set(input)
  message[input.length] = 0x80
  const bitLength = BigInt(input.length) * 8n
  for (let index = 0; index < 8; index += 1) {
    message[message.length - 8 + index] = Number((bitLength >> BigInt(index * 8)) & 0xffn)
  }

  let stateA = 0x67452301
  let stateB = 0xefcdab89
  let stateC = 0x98badcfe
  let stateD = 0x10325476
  const view = new DataView(message.buffer)

  for (let offset = 0; offset < message.length; offset += 64) {
    const words = Array.from({ length: 16 }, (_, index) => view.getUint32(offset + index * 4, true))
    let a = stateA
    let b = stateB
    let c = stateC
    let d = stateD

    for (let index = 0; index < 64; index += 1) {
      let mixed: number
      let wordIndex: number
      if (index < 16) {
        mixed = (b & c) | (~b & d)
        wordIndex = index
      } else if (index < 32) {
        mixed = (d & b) | (~d & c)
        wordIndex = (5 * index + 1) % 16
      } else if (index < 48) {
        mixed = b ^ c ^ d
        wordIndex = (3 * index + 5) % 16
      } else {
        mixed = c ^ (b | ~d)
        wordIndex = (7 * index) % 16
      }
      const next = (a + (mixed >>> 0) + constants[index] + words[wordIndex]) >>> 0
      a = d
      d = c
      c = b
      b = (b + rotateLeft(next, shifts[index])) >>> 0
    }

    stateA = (stateA + a) >>> 0
    stateB = (stateB + b) >>> 0
    stateC = (stateC + c) >>> 0
    stateD = (stateD + d) >>> 0
  }

  return [stateA, stateB, stateC, stateD].map(littleEndianHex).join('')
}
