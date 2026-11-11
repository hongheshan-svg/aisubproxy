// F1-16：只留一套提示条（dialog.toast，颜色由皮肤决定），旧的写死颜色的 lib/ui.ts 删掉
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const path = (p: string) => fileURLToPath(new URL(p, import.meta.url))

describe('提示条只有一套', () => {
  it('lib/ui.ts 已删除', () => expect(existsSync(path('../ui.ts'))).toBe(false))
  it('卡密查询页和售后结果卡改用 dialog.toast，不再引入 lib/ui', () => {
    for (const f of ['../../views/user/CDKStatusView.vue', '../../components/AfterSaleResult.vue']) {
      const src = readFileSync(path(f), 'utf8')
      expect(src, f).not.toMatch(/lib\/ui['"]/)
      expect(src, f).toMatch(/dialog\.toast\(/)
    }
  })
})
