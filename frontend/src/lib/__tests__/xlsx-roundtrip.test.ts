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

  it('有结果未知的卡密时另起一页「结果未知」，带卡密和说明；成功页不变（F2-03 裁定 1）', async () => {
    tmp = mkdtempSync(join(tmpdir(), 'xlsx-rt-'))
    process.chdir(tmp)
    const ok: Array<[string, string, string, string]> = [['a@example.com', 'gpt-pass-1', 'mail-pass-1', 'token-aaa']]
    const unknown = [['SXC-AAAA-BBBB', 'b@example.com', 'gpt-pass-2', 'mail-pass-2', 'token-bbb', '结果未知，请到卡密查询页核对，不要重复提交']]
    await exportSuccessWorkbook(ok, 'rt', unknown)
    const files = readdirSync(tmp).filter((f) => f.startsWith('rt_') && f.endsWith('.xlsx'))
    const wb = XLSX.read(readFileSync(join(tmp, files[0])))
    expect(wb.SheetNames).toEqual(['账号', '结果未知'])
    const rowsOf = (name: string) => XLSX.utils.sheet_to_json(wb.Sheets[name], { header: 1, defval: '' })
    expect(rowsOf('账号')).toEqual([['邮箱', 'GPT密码', '邮箱密码', 'at'], ...ok])
    expect(rowsOf('结果未知')).toEqual([['卡密', '邮箱', 'GPT密码', '邮箱密码', 'at', '状态'], ...unknown])
  })

  it('没有结果未知的卡密时只有「账号」一页', async () => {
    tmp = mkdtempSync(join(tmpdir(), 'xlsx-rt-'))
    process.chdir(tmp)
    await exportSuccessWorkbook([['a@example.com', '', '', '']], 'rt')
    const files = readdirSync(tmp).filter((f) => f.startsWith('rt_') && f.endsWith('.xlsx'))
    expect(XLSX.read(readFileSync(join(tmp, files[0]))).SheetNames).toEqual(['账号'])
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
