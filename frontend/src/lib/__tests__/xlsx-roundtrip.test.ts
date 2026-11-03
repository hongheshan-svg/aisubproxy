import { describe, it, expect, afterEach } from 'vitest'
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import * as fs from 'node:fs'
import * as XLSX from 'xlsx'
import { exportSuccessWorkbook, readWorkbookRows } from '../batch-session'

// vite 解析到 ESM 版 xlsx，默认没有文件系统，writeFile 需要显式注入 fs
XLSX.set_fs(fs)

const origCwd = process.cwd()
let tmp = ''

afterEach(() => {
  process.chdir(origCwd)
  if (tmp) rmSync(tmp, { recursive: true, force: true })
  tmp = ''
})

describe('xlsx 往返', () => {
  it('exportSuccessWorkbook 写出的文件可被 readWorkbookRows 读回，表头与内容一致', async () => {
    tmp = mkdtempSync(join(tmpdir(), 'xlsx-rt-'))
    process.chdir(tmp)
    const rows: Array<[string, string, string, string]> = [
      ['a@example.com', 'gpt-pass-1', 'mail-pass-1', 'token-aaa'],
      ['b@example.com', 'gpt-pass-2', 'mail-pass-2', 'token-bbb'],
    ]
    await exportSuccessWorkbook(rows, 'rt')
    const files = readdirSync(tmp).filter((f) => f.startsWith('rt_') && f.endsWith('.xlsx'))
    expect(files).toHaveLength(1)
    const file = new File([readFileSync(join(tmp, files[0]))], 'rt.xlsx')
    const got = await readWorkbookRows(file)
    expect(got[0]).toEqual(['邮箱', 'GPT密码', '邮箱密码', 'at'])
    expect(got.slice(1)).toEqual(rows)
  })

  it('XLSX.write 生成的工作簿（含「账号」页名）可读回', async () => {
    const sheet = XLSX.utils.aoa_to_sheet([
      ['邮箱', 'x'],
      ['c@example.com', '中文值'],
    ])
    const wb = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(wb, sheet, '账号')
    const out = XLSX.write(wb, { type: 'array', bookType: 'xlsx' }) as ArrayBuffer
    const got = await readWorkbookRows(new File([out], 'a.xlsx'))
    expect(got).toEqual([
      ['邮箱', 'x'],
      ['c@example.com', '中文值'],
    ])
  })
})
