import { readFile, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const catalogPath = join(repositoryRoot, 'web/src/report/analysis/analysis-card-catalog.json')
const manifestDirectory = join(repositoryRoot, 'internal/report/template/manifests')
const profilePath = join(repositoryRoot, 'internal/report/template/editor-profiles.json')

const catalog = JSON.parse(await readFile(catalogPath, 'utf8'))

function cardinality(groups, kind) {
  return groups.filter(group => group.kind === kind).reduce((result, group) => ({
    min: result.min + group.min,
    max: result.max + group.max,
  }), { min: 0, max: 0 })
}

function manifestFor(item) {
  const dimensions = cardinality(item.bindingGroups, 'DIMENSION')
  const measures = cardinality(item.bindingGroups, 'MEASURE')
  const roles = [...new Set(item.bindingGroups.flatMap(group => group.roles))]
  const isTable = item.category === 'TABLE'
  const isContent = item.category === 'CONTENT'
  return {
    type: item.type,
    version: '1.0.0',
    renderer: 'REACT',
    displayName: item.name.replace(/类$/, '卡'),
    category: item.category,
    minSize: { w: isTable ? 10 : 6, h: isTable ? 5 : isContent ? 3 : 4 },
    recommendedSize: { w: isTable ? 18 : 12, h: isTable ? 8 : isContent ? 5 : 6 },
    dataContract: {
      dimensions,
      measures,
      timeField: { required: false },
      roles,
    },
    stackingRequiresAdditive: false,
    optionSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['cardVariant'],
      properties: {
        title: { type: 'string' },
        subtitle: { type: 'string' },
        cardVariant: { type: 'string', description: '卡片版式', enum: ['01', '02', '03'] },
        showLegend: { type: 'boolean', description: '显示图例' },
        showLabel: { type: 'boolean', description: '显示数值标签' },
        numberFormat: { type: 'string', description: '数字格式' },
        topN: { type: 'integer', description: '最多显示对象数', minimum: 1, maximum: 100 },
      },
    },
    defaultOptions: { cardVariant: '01', showLegend: true, showLabel: true, topN: 10 },
    mobilePolicy: { supported: true, defaultLegendMode: 'HIDDEN', labelDegradation: 'ELLIPSIS' },
    supportedInteractions: isContent ? [] : ['CLICK_FILTER'],
  }
}

for (const item of catalog) {
  const target = join(manifestDirectory, `${item.type}.json`)
  await writeFile(target, `${JSON.stringify(manifestFor(item), null, 2)}\n`)
}

const profileDocument = JSON.parse(await readFile(profilePath, 'utf8'))
profileDocument.profiles = profileDocument.profiles.filter(profile => !profile.componentType.startsWith('analysis-'))
for (const item of catalog) {
  profileDocument.profiles.push({
    componentType: item.type,
    componentVersion: '1.0.0',
    example: {
      title: item.name.replace(/类$/, '卡'),
      description: item.question,
      items: [
        `常见子类型：${item.subtypes.slice(0, 4).join('、')}`,
        `常用呈现：${item.presentations.join('、')}`,
      ],
    },
    bindingGroups: item.bindingGroups,
  })
}
await writeFile(profilePath, `${JSON.stringify(profileDocument, null, 2)}\n`)

console.log(`generated ${catalog.length} analysis card manifests and editor profiles`)
