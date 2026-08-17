import type { AnalysisTemplate } from './model.ts'
import { createMonthlyOperatingAnalysisTemplate } from './monthly-operating-template.ts'

/**
 * 内置模板目录只保留经营分析报告；模板设计本身以对应 JSON 文件为唯一真源。
 */
export const analysisTemplateFixtures: AnalysisTemplate[] = [
  createMonthlyOperatingAnalysisTemplate(),
]
