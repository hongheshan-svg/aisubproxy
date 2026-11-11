# 界面统一与电脑布局（子项目 B）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 皮肤同时管用户端和后台（新增默认皮肤「深青自助」、去掉主色），后台和代理商后台换深色侧栏，用户端在电脑上改为两栏加步骤条。

**Architecture:** 皮肤的颜色变量（`--bg`、`--ink`、`--primary` 等）是唯一来源，用户端的 `--u-*` 改为指向它们；`theme.ts` 套用皮肤时额外写 `data-tone` 和侧栏颜色变量。管理后台和代理商后台共用一个新的侧栏外壳组件；用户端加导航条、两栏组件和共用的常见问题组件，由一个按 `matchMedia` 更新的共享状态决定右栏内容放哪。

**Tech Stack:** Vue 3 + vue-router + vue-i18n + Element Plus + Tailwind 3，vitest（无 DOM，组件用 `vue/server-renderer` 渲染断言）；Go + gin（后端设置接口）。

**Spec:** `docs/superpowers/specs/2026-11-11-ui-unify-desktop-design.md`

## Global Constraints

- 所有界面文字、注释、提交信息用简体中文；提交信息末尾加一行 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- 不新增 npm 或 Go 依赖。测试环境没有 DOM：组件测试照 `src/views/__tests__/home.spec.ts`、`src/layouts/__tests__/AdminLayout.spec.ts` 的做法用 `vue/server-renderer`；需要 `localStorage`、`window`、`document` 时照 `src/__tests__/theme.test.ts` 装假对象。
- 工作区 `frontend/` 下第一次跑测试前先 `npm ci`。前端测试命令：`cd frontend && npx vitest run <路径>`；后端：`cd backend && go test ./internal/handler -run <名字> -count=1`。
- `git add` 只写明确路径，不用 `-A`；不提交 `.claude/`、`.superpowers/`；不用裸 `git stash`。
- 不碰 18080 端口和仓库根目录的 `data/`。
- 新皮肤编号 `teal`，中文名「深青自助」，英文名「Teal Kiosk」，排在皮肤列表第一位，也是默认皮肤。
- 电脑布局的判断条件是 `(min-width: 1024px)`；内容区最宽 1120px，左右至少 24px；两栏 `minmax(0,3fr) minmax(0,2fr)`，栏间距 32px。
- 用户端不加载 Google 字体，字体固定为 `-apple-system, "PingFang SC", "Microsoft YaHei", "Noto Sans SC", sans-serif`，等宽 `"SF Mono", "JetBrains Mono", Menlo, Consolas, monospace`。
- 用户端按钮是纯色：底 `--u-primary`、字 `--u-on-primary`，不用 `--btn-img`、`--btn-shadow` 一类皮肤按钮装饰。
- 用户端对比度要求（每套皮肤、亮暗两种）：`--ink` 在 `--bg`、`--surface-solid` 上，`--ink-2` 在 `--bg`、`--surface-solid` 上，`--primary-on` 在 `--primary` 上，`--primary` 在 `--surface-solid` 上，都不低于 4.5:1。
- 侧栏颜色：side-bg 保持主色色相和饱和度、HSL 亮度每次降 0.02，直到白字对比度不低于 10:1；side-ink = 白 78% + side-bg 22%；side-muted = 白 60% + side-bg 40%。深青自助亮色结果：`#0A473F`、`#C9D7D5`、`#9DB5B2`。
- 侧栏选中项：主色底、皮肤按钮字色、`font-weight: 700`、左边 3px 白色竖线（`box-shadow: inset 3px 0 0 #FFFFFF`）。
- 新增文案只有导航条：`redeemUi.nav.redeem` 中文「兑换」、英文「Redeem」；`redeemUi.nav.label` 中文「主导航」、英文「Main navigation」。其余导航文字沿用 `redeemUi.home.footerBatch`、`footerBilling`、`footerFaq`。
- 「外观与品牌」页说明文字逐字为：「站点名称、副标题、Logo、默认亮暗模式和皮肤。保存后用户端、后台和代理商后台一起生效；没保存的皮肤只是预览，刷新或离开本页会还原。」皮肤一项的标签逐字为「皮肤（用户端和后台一起换）」。
- 本地存储键：`theme-mode`、`site-skin`、`site-brand`、`ops-sidenav` 沿用；代理商后台折叠偏好新用 `agent-sidenav`。所有读写包 try/catch。

## Review Focus

1. **在「粘贴登录信息」这一步把窗口拉宽或拉窄，跨过 1024px**：已粘贴的登录信息、已勾选的点数须知不能丢（右栏的设备说明重新选一次页签可以接受）。任务 7 加结构断言；任务 12 浏览器里实测。
2. **Logo 地址失效（图片加载失败）**：导航条、侧栏、登录卡片只显示站名，不显示破图。任务 6、9、11 各自给图片加加载失败的处理并有断言；任务 12 浏览器里把 Logo 删掉再看缓存图。
3. **站名很长（30 个汉字）**：导航条、侧栏、登录卡片里用省略号截断，页面不出现横向滚动条。任务 6、9、11 的样式里写截断；任务 12 在 1024 宽实测。
4. **宽度正好 1024px 是电脑布局，1023px 是单栏**：脚本里的查询条件和样式里的媒体查询必须是同一个数。任务 6 加断言。
5. **在「外观与品牌」点了别的皮肤预览但没保存就离开**：前后台都回到已保存的皮肤，本机记住的皮肤不变。任务 5 加断言；任务 12 浏览器里实测。

---

### Task 1: 后端：默认皮肤改 teal，不再处理主色

**Files:**
- Modify: `backend/internal/handler/settings.go`
- Delete: `backend/internal/handler/site_color.go`（删完主色处理后其中函数都不再被用，lint 会报 unused）
- Modify: `backend/internal/handler/site_color_test.go`（删掉 `TestContrastRatio`、`TestValidPrimaryColor`、`TestPutPrimaryColor`，加下面的新测试）

**Interfaces:**
- Produces: `GET /api/v1/public/site`、`GET /api/v1/admin/settings` 返回里没有 `primary_color`，`skin` 默认 `"teal"`；`PUT /api/v1/admin/settings` 接受 `skin: "teal"`，忽略 `primary_color`。

- [ ] **Step 1: 写失败的测试**（放在 `site_color_test.go`，复用现有的 `s1Put`、`publicSiteJSON`、`mwDo`）

```go
func TestPublicSiteDefaults(t *testing.T) {
	dbtest.Open(t)
	m := publicSiteJSON(t)
	if m["skin"] != "teal" || m["theme_mode"] != "auto" || m["logo_version"] != "" {
		t.Fatalf("默认值不对: %v", m)
	}
	if _, ok := m["primary_color"]; ok {
		t.Fatalf("不应再返回 primary_color: %v", m)
	}
}

func TestAdminGetSettingsNoPrimary(t *testing.T) {
	dbtest.Open(t)
	w := mwDo(AdminGetSettings, "GET", "/x", "", nil)
	// 解析 JSON：skin == "teal"，没有 primary_color 键
}

func TestPutSkinTeal(t *testing.T) {
	dbtest.Open(t)
	// s1Put(`{"skin":"teal"}`) → 200，db skin == "teal"，公开接口 skin == "teal"
	// s1Put(`{"skin":"nope"}`) → 400
}

func TestPutIgnoresPrimaryColor(t *testing.T) {
	dbtest.Open(t)
	_ = db.SetSetting("primary_color", "#1E4FA8")
	// 不合格的颜色也不能让整批失败：
	// s1Put(`{"primary_color":"#7FD4C4","brand_name":"新站名"}`) → 200
	// db brand_name == "新站名"；db primary_color 仍是 "#1E4FA8"
	// s1Put(`{"primary_color":"#0F6B5F"}`) → 200，db primary_color 仍是 "#1E4FA8"
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/handler -run 'TestPublicSiteDefaults|TestAdminGetSettingsNoPrimary|TestPutSkinTeal|TestPutIgnoresPrimaryColor' -count=1`
Expected: FAIL（skin 是 zovo、返回里有 primary_color、teal 被拒、不合格颜色返回 400）

- [ ] **Step 3: 改 `settings.go`**
  - `defaultSiteSkin = "teal"`；删除 `defaultPrimaryColor`；更新文件顶部注释（默认皮肤是深青自助）。
  - 两个读接口去掉 `"primary_color"`。
  - `adminSettingsBody` 删掉 `PrimaryColor` 字段（gin 解析 JSON 时忽略未知字段）；删掉保存时校验和写入主色的代码。
  - `allowedSkins` 加 `"teal": true`。
  - 删除 `site_color.go`。

- [ ] **Step 4: 跑测试确认通过，再跑整个包和静态检查**

Run: `cd backend && go test ./internal/handler -count=1 && go vet ./... && golangci-lint run ./internal/handler/...`
Expected: PASS，lint 0 issues

- [ ] **Step 5: 提交**

```bash
F=(backend/internal/handler/settings.go backend/internal/handler/site_color.go backend/internal/handler/site_color_test.go)
git add "${F[@]}" && git commit -m "feat(settings): 默认皮肤改为深青自助，不再处理主色"
```

---

### Task 2: 主题核心：皮肤按亮暗记主色、新增深青自助、data-tone、侧栏颜色

**Files:**
- Modify: `frontend/src/theme.ts`
- Modify: `frontend/src/lib/brandColor.ts`（只新增，不删）
- Modify: `frontend/src/style.css`（新增 teal 两块）
- Modify: `frontend/index.html`（首屏脚本）
- Test: `frontend/src/__tests__/theme.test.ts`、`frontend/src/lib/__tests__/brandColor.test.ts`

**Interfaces:**
- Produces（`theme.ts`）：
  - `export interface SkinTone { primary: string; on: string }`
  - `SkinMeta` 去掉 `primary`、`primaryOn`、`primaryLight`、`primaryOnLight`，改为 `light: SkinTone; dark: SkinTone`。每套现有皮肤的值取自 `style.css` 里该皮肤亮色块和暗色块的 `--primary`、`--primary-on`（没有暗色块的，dark 等于 light；`preferDark` 的皮肤两者都取它自己那块）。任务 3 的测试会逐一核对。
  - `export const DEFAULT_SKIN: SkinId = 'teal'`；`SkinId` 加 `'teal'`；`SKINS[0]` 是 teal。
  - `export function effectiveDark(mode: ThemeMode, skin: SkinMeta, systemDark: boolean): boolean`：`mode==='dark'`、或 `mode==='auto' && systemDark`、或 `skin.preferDark` 时为真。
  - `export function skinTone(skin: SkinMeta, dark: boolean): SkinTone`
  - 套用皮肤时（现有 `applySkin`）：写 `data-tone` 为 `dark`/`light`（按 `effectiveDark`）；Element Plus 用 `skinTone` 的 primary/on；在根元素上写 `--side-bg`、`--side-ink`、`--side-muted`（来自 `sideColorsFrom(tone.primary)`）、`--side-active`（tone.primary）、`--side-active-on`（tone.on）。
  - `ensureSkinFonts`：皮肤 `fonts` 为空时不插入任何链接（连等宽字体也不加）。
- Produces（`brandColor.ts`）：`export function sideColorsFrom(primary: string): { bg: string; ink: string; muted: string }`，用文件里现有的 `rgbToHsl`、`hslToRgb`、`format`、`contrastRatio`、`mixHex`。
- teal 皮肤元数据：`label '深青自助'`、`labelEn 'Teal Kiosk'`、`swatch '#0F6B5F'`、`swatch2 '#EEF5F3'`、`blurb '深青自助 · 大字大按钮'`、`heading 'sans'`、`density 'comfy'`、`nav 'block'`、`light {#0F6B5F, #FFFFFF}`、`dark {#3FB8A5, #0C1A18}`、`fonts []`。
- `SiteBrand.primary` 本任务先保留（任务 4 删）。

- [ ] **Step 1: 写失败的测试**

`brandColor.test.ts` 新增：

```ts
it('侧栏颜色：深青固定值', () => {
  expect(sideColorsFrom('#0F6B5F')).toEqual({ bg: '#0A473F', ink: '#C9D7D5', muted: '#9DB5B2' })
})
it('侧栏颜色：各种主色都达标', () => {
  for (const p of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F', '#767676', '#05080A']) {
    const s = sideColorsFrom(p)
    expect(contrastRatio('#FFFFFF', s.bg)).toBeGreaterThanOrEqual(10)
    expect(contrastRatio(s.ink, s.bg)).toBeGreaterThanOrEqual(4.5)
    expect(contrastRatio(s.muted, s.bg)).toBeGreaterThanOrEqual(4.5)
  }
})
```

`theme.test.ts` 新增（假对象里把 `setAttribute`、`style.setProperty` 的调用记下来）：

```ts
it('皮肤列表第一项是 teal，没记过皮肤时用 teal', ...)        // SKINS[0].id === 'teal'; siteSkin.value === 'teal'
it('记了不存在的皮肤编号时用 teal', ...)                     // localStorage site-skin = 'bogus'
it('记了 zovo 就用 zovo', ...)
it('effectiveDark 四种情况', () => {
  // (light, forest, false) false；(dark, forest, false) true；(light, cyber, false) true；(auto, forest, true) true
})
it('套用 teal 亮色：写 data-tone 和侧栏变量', () => {
  // data-skin=teal、data-tone=light；--side-bg #0A473F、--side-ink #C9D7D5、--side-muted #9DB5B2、
  // --side-active #0F6B5F、--side-active-on #FFFFFF
})
it('默认暗色皮肤选亮色时 data-tone 仍是 dark', ...)          // setTheme('light') + setSkin('cyber')
it('teal 不加载任何 Google 字体', ...)                       // setSkinFontsAllowed(true) 后 appended.length === 0
```

现有引用 `primaryLight` 等旧字段的断言（约第 61–66 行）改成新字段。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/__tests__/theme.test.ts src/lib/__tests__/brandColor.test.ts`
Expected: FAIL（`sideColorsFrom`、`effectiveDark` 不存在，默认皮肤是 zovo）

- [ ] **Step 3: 实现**
  - `brandColor.ts` 加 `sideColorsFrom`。
  - `theme.ts` 按上面的接口改；`readSkin` 无效或缺省时返回 `DEFAULT_SKIN`；更新文件里写着「默认 zovo / 曜夜雾金」的注释。
  - `style.css` 在多皮肤预设一节加 `html[data-skin="teal"]` 和 `html.dark[data-skin="teal"]`，值逐字取规格 §3.1 的表，另加 `--surface-solid`（同 `--surface`）、`--row-hover: var(--primary-soft)`、`--bg-tint: none`、`--radius-sm: 8px`、`--radius-md: 14px`、`--radius-lg: 14px`，以及把 `--font-sans`、`--font-display`、`--font-serif` 设为 Global Constraints 里的系统字体。
  - `index.html`：默认皮肤 `'teal'`，注释同步；在已有的暗色判断之后加 `document.documentElement.setAttribute('data-tone', dark ? 'dark' : 'light')`。

- [ ] **Step 4: 跑测试确认通过，再跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS（只改了皮肤数据结构的引用处若有失败，按新字段修）

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/theme.ts frontend/src/lib/brandColor.ts frontend/src/style.css frontend/index.html frontend/src/__tests__/theme.test.ts frontend/src/lib/__tests__/brandColor.test.ts)
git add "${F[@]}" && git commit -m "feat(theme): 新增深青自助皮肤并设为默认，皮肤按亮暗记主色，加 data-tone 与侧栏颜色"
```

---

### Task 3: 12 套皮肤的对比度测试与修色

**Files:**
- Create: `frontend/src/styles/__tests__/cssTokens.ts`（只给测试用的解析工具）
- Create: `frontend/src/styles/__tests__/skinContrast.test.ts`
- Modify: `frontend/src/style.css`（只改不达标皮肤的颜色值）
- Modify: `frontend/src/theme.ts`（同步改 `SKINS` 里对应的 `light`/`dark`）

**Interfaces:**
- Consumes: 任务 2 的 `SKINS`（含 `light`、`dark`）、`sideColorsFrom`、`contrastRatio`。
- Produces（测试工具，不进产品代码）：
  - `parseSkinTokens(css: string): { root: Record<string, string>; skins: Record<string, { light: Record<string, string>; dark: Record<string, string> }> }`
  - `effectiveTokens(parsed, skin: string, mode: 'light' | 'dark'): Record<string, string>`：亮色 = root 叠皮肤亮色块；暗色 = 亮色再叠皮肤暗色块。
  - `resolveHex(tokens, name: string): string`：顺着 `var(--x)` 和 `var(--x, 兜底)` 解析到底，`#abc` 展开成大写 `#AABBCC`；解析不到十六进制色（如 `rgba`）就抛错并写出变量名。

解析规则：先去掉注释；只看顶层规则（跳过 `@media` 等块内的规则）；选择器按逗号拆开，`:root` 归 root，`html[data-skin="X"]` 归 X 的亮色，`html.dark[data-skin="X"]` 归 X 的暗色，其余忽略；只收 `--` 开头的声明；后出现的覆盖先出现的。输入是 `style.css` 和 `styles/skins.css` 按 `main.ts` 的引入顺序拼起来（用 `fs.readFileSync` 读）。

- [ ] **Step 1: 写失败的测试**

```ts
const PAIRS: Array<[string, string]> = [
  ['--ink', '--bg'], ['--ink', '--surface-solid'], ['--ink-2', '--bg'], ['--ink-2', '--surface-solid'],
  ['--primary-on', '--primary'], ['--primary', '--surface-solid'],
]
for (const s of SKINS) for (const mode of ['light', 'dark'] as const) {
  it(`${s.id} ${mode} 对比度`, () => {
    const tk = effectiveTokens(parsed, s.id, mode)
    const bad = PAIRS.map(([a, b]) => [a, b, contrastRatio(resolveHex(tk, a), resolveHex(tk, b))] as const)
      .filter(([, , r]) => r < 4.5)
      .map(([a, b, r]) => `${a} 在 ${b} 上 ${r.toFixed(2)}`)
    expect(bad, `${s.id} ${mode}`).toEqual([])
  })
  it(`${s.id} ${mode} 元数据和样式一致`, () => {
    // resolveHex(tk,'--primary') === s[mode].primary.toUpperCase()；--primary-on 同理
  })
  it(`${s.id} ${mode} 侧栏颜色达标`, () => {
    // sideColorsFrom(s[mode].primary)：白字 ≥10，ink、muted ≥4.5
  })
}
it('解析工具：var 链、三位色号、rgba 报错', ...)   // 用一小段手写 CSS 测 parseSkinTokens/resolveHex
```

- [ ] **Step 2: 跑测试，记下全部失败项**

Run: `cd frontend && npx vitest run src/styles/__tests__/skinContrast.test.ts`
Expected: FAIL，至少包括 forest light `--primary-on 在 --primary 上 3.77`、zovo light `3.85`。把完整失败清单写进任务报告。

- [ ] **Step 3: 修色**

对每个失败项，只改那套皮肤那个模式的那个变量：保持色相和饱和度，HSL 亮度每次 0.02，亮色模式往深调、暗色模式往浅调，调到该皮肤所有相关的对都达标为止（`--primary` 一改要同时满足按钮字和链接两对）。同一个值若同时写在该皮肤块里的 `--el-color-primary`，一起改；`--primary-hover`、`--primary-active` 按同样的亮度差跟着挪。`theme.ts` 里该皮肤的 `light`/`dark` 同步改。报告里列一张「皮肤 / 模式 / 变量 / 原值 → 新值」的表，任务 12 写进 CHANGELOG。

- [ ] **Step 4: 跑测试确认通过，再跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/styles/__tests__/cssTokens.ts frontend/src/styles/__tests__/skinContrast.test.ts frontend/src/style.css frontend/src/theme.ts)
git add "${F[@]}" && git commit -m "fix(theme): 12 套皮肤亮暗两种的文字和按钮对比度补到 4.5，并加测试"
```

---

### Task 4: 用户端从皮肤取颜色

**Files:**
- Modify: `frontend/src/styles/user-theme.css`
- Modify: `frontend/src/components/redeem/UserShell.vue`、`frontend/src/components/DialogHost.vue`、`frontend/src/main.ts`、`frontend/src/theme.ts`（删 `SiteBrand.primary`）
- Modify: `frontend/src/lib/brandColor.ts`（删 `brandColorVars`、`tintFrom`、`darkPrimaryFrom` 及只被它们用的常量；`isUsablePrimary` 留给任务 5 删）
- Modify: 用户端组件里写死的 `border-radius: 14px`（`grep -rn "border-radius: 14px" frontend/src/components frontend/src/views/user` 找全），改为 `var(--u-radius)`（卡片类用 `var(--u-radius-card)`）
- Test: Create `frontend/src/styles/__tests__/userTheme.test.ts`；Modify `frontend/src/lib/__tests__/brandColor.test.ts`（删掉被删函数的用例）；按需改 `frontend/src/views/__tests__/visual-*.test.ts` 里断言旧写法的地方

**Interfaces:**
- Produces: `.user-ui` 上的 `--u-bg: var(--bg)`、`--u-surface: var(--surface-solid)`、`--u-ink: var(--ink)`、`--u-muted: var(--ink-2)`、`--u-line: var(--brd)`、`--u-primary: var(--primary)`、`--u-on-primary: var(--primary-on)`、`--u-tint: var(--primary-soft)`、`--u-radius: var(--radius-md)`、`--u-radius-card: var(--radius-lg)`；提醒、出错、禁用六个变量亮色写在 `.user-ui`，暗色写在 `html[data-tone="dark"] .user-ui`（值逐字取 `user-theme.css` 现有的亮、暗两套）。

- [ ] **Step 1: 写失败的测试** `userTheme.test.ts`（读 `user-theme.css` 文本）：
  - `.user-ui { … }` 块里上面 10 个映射逐字存在，且这 8 个颜色变量的值里没有 `#`；
  - 有 `html[data-tone="dark"] .user-ui` 块，里面有 `--u-warn-bg: #2E2410`；
  - 文件里没有 `prefers-color-scheme`、没有 `[data-theme`；`.user-ui` 块里没有 `background: var(--u-bg)`；
  - `.user-ui` 的 `font-family` 是 Global Constraints 里的系统字体；
  - 文件里不再出现 `border-radius: 14px`。

  再加一个 SSR 用例：渲染 `UserShell`（默认插槽放一段文字），HTML 里没有 `data-theme`，没有 `--brand-primary`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/styles/__tests__/userTheme.test.ts`
Expected: FAIL

- [ ] **Step 3: 实现**
  - `user-theme.css` 按 Interfaces 改；`.user-ui` 底色改 `transparent`，让 `html` 上皮肤的底色和背景装饰透出来；`.u-card`、`.u-input`、`.u-btn-*`、`.u-warn`/`.u-error` 的圆角改用变量；按钮保持纯色。
  - `UserShell.vue` 只留 `<div class="user-ui min-h-screen"><slot /></div>`。
  - `DialogHost.vue` 用户端弹窗只保留 `user-ui` 类，去掉 `data-theme` 和主色样式。
  - `main.ts` 去掉 `setSiteBrand({ primary })`，Logo 版本照旧。
  - `theme.ts` 的 `SiteBrand`、`loadBrand`、`setSiteBrand` 去掉 `primary`。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**（路径按实际改动列全）

```bash
git add frontend/src/styles/user-theme.css frontend/src/styles/__tests__/userTheme.test.ts frontend/src/components/redeem/UserShell.vue frontend/src/components/DialogHost.vue frontend/src/main.ts frontend/src/theme.ts frontend/src/lib/brandColor.ts frontend/src/lib/__tests__/brandColor.test.ts <其余改了圆角的组件>
git commit -m "feat(user-ui): 用户端颜色和圆角改为从皮肤取，提醒类颜色按 data-tone 分亮暗"
```

---

### Task 5: 「外观与品牌」去掉主色

**Files:**
- Modify: `frontend/src/views/admin/SiteAppearance.vue`、`frontend/src/lib/siteAppearance.ts`、`frontend/src/lib/brandColor.ts`（删 `isUsablePrimary`，若已无人用）
- Test: `frontend/src/lib/__tests__/siteAppearance.test.ts`；Create `frontend/src/views/__tests__/site-appearance.test.ts`

**Interfaces:**
- Produces（`siteAppearance.ts`）：删除 `PRESET_COLORS`、`PRIMARY_TOO_LIGHT`、`PRIMARY_BAD_FORMAT`、`PrimaryCheck`、`checkPrimary`；`FormState` 去掉 `primary_color`；新增 `export function buildSavePayload(form: FormState, skin: SkinId, mode: ThemeMode): { brand_name: string; brand_sub: string; skin: SkinId; theme_mode: ThemeMode }`，`SiteAppearance.vue` 保存时用它。

- [ ] **Step 1: 写失败的测试**
  - `buildSavePayload` 返回的键正好是 `brand_name`、`brand_sub`、`skin`、`theme_mode`；
  - `applySettings` 收到带 `primary_color` 的数据时，返回的表单状态里没有这个键；
  - 原有 `checkLogoFile`、站名、副标题、皮肤、`logo_version` 的用例保留；删掉 `checkPrimary` 的用例；
  - `site-appearance.test.ts` 读 `SiteAppearance.vue` 源文件：不含 `primary_color`、`PRESET_COLORS`、`checkPrimary`；含 Global Constraints 里的页面说明原文和「皮肤（用户端和后台一起换）」；离开页面时仍调用还原已保存皮肤的逻辑（现有的 `if (dirty.value) setSkin(savedSkin.value)` 一类语句还在）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/siteAppearance.test.ts src/views/__tests__/site-appearance.test.ts`
Expected: FAIL

- [ ] **Step 3: 实现**：删掉页面上主色一块（预设、色号输入、校验提示、预览），改页面说明和皮肤标签，保存用 `buildSavePayload`。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/views/admin/SiteAppearance.vue frontend/src/lib/siteAppearance.ts frontend/src/lib/brandColor.ts frontend/src/lib/__tests__/siteAppearance.test.ts frontend/src/views/__tests__/site-appearance.test.ts)
git add "${F[@]}" && git commit -m "feat(appearance): 外观与品牌去掉主色，皮肤改为前后台一起换"
```

---

### Task 6: 用户端布局零件：电脑布局状态、常见问题、两栏、导航条

**Files:**
- Create: `frontend/src/lib/layoutMode.ts`、`frontend/src/components/redeem/FaqList.vue`、`frontend/src/components/redeem/UserTwoColumn.vue`、`frontend/src/components/redeem/UserTopNav.vue`
- Modify: `frontend/src/components/UserPageHeader.vue`（顶部那一行改用 `UserTopNav`）、`frontend/src/main.ts`（启动时调 `initLayoutMode()`）、`frontend/src/styles/user-theme.css`（加 `.u-page--split`）、`frontend/src/i18n/locales/zh.ts`、`frontend/src/i18n/locales/en.ts`
- Test: Create `frontend/src/components/redeem/__tests__/layout.spec.ts`

**Interfaces:**
- Produces:
  - `layoutMode.ts`：`export const DESKTOP_QUERY = '(min-width: 1024px)'`；`export const isDesktop = ref(false)`；`export function initLayoutMode(win?: { matchMedia?: (q: string) => MediaQueryList }): void`（不传参数时用全局 `window`；没有 `matchMedia` 时什么也不做、不抛错；监听 `change` 更新 `isDesktop`）。
  - `FaqList.vue`：无参数；渲染 `<section id="help" class="faq" aria-labelledby="faq-title">`，标题和 7 条问答原样从 `HomeView.vue` 搬过来（含 `session`、`claude` 两条的特殊内容和样式）。
  - `UserTwoColumn.vue`：默认插槽是主栏，具名插槽 `aside`、`wide`；结构 `<div class="u-cols"><div class="u-col-main"><slot/></div><aside v-if="$slots.aside" class="u-col-aside"><slot name="aside"/></aside></div><div v-if="$slots.wide" class="u-wide"><slot name="wide"/></div>`。样式：默认一栏、`aside` 上边距 32px；`@media (min-width: 1024px)` 时 `.u-cols` 用两栏网格（Global Constraints）、`align-items: start`、`aside` 无上边距。
  - `UserTopNav.vue`：参数 `home?: boolean`、`back?: boolean`。左边 Logo（`siteBrand.logoVersion` 有值且没加载失败时显示，`@error` 后隐藏）和站名（`router-link` 到 `/`，过长省略号截断）；`isDesktop` 为真时中间是 `<nav :aria-label="t('redeemUi.nav.label')">` 四个链接：`/`（`redeemUi.nav.redeem`）、`/batch`、`/billing`、常见问题（`home` 时 `#help`，否则 `/#help`）；当前页的链接带 `aria-current="page"` 和加粗主色样式，路径是 `/` 或 `/recharge` 时「兑换」算当前页；右边 `LanguageToggle`、`ThemeToggle`；「返回首页」只在 `back && !isDesktop` 时显示。电脑布局下整条 `--u-surface` 底、下边 `--u-line` 分隔线，内层最宽 1120px。
  - `.u-page--split`：`@media (min-width: 1024px)` 时 `max-width: 1120px; padding-left/right: 24px`。
  - `UserPageHeader.vue`：参数不变，另加 `home?: boolean` 传给 `UserTopNav`；标题区在电脑布局下也放宽到 1120px。

- [ ] **Step 1: 写失败的测试** `layout.spec.ts`（照 `home.spec.ts` 搭路由和 i18n；每个用例前设 `isDesktop.value`）：

```ts
it('电脑布局在 /batch：四个链接，批量兑换是当前页，不显示返回首页', ...)
// HTML 含 href="/"、href="/batch"、href="/billing"、href="/#help"；/batch 那个 <a> 带 aria-current="page"；
// back=true 时也没有「返回首页」文字
it('电脑布局在 /recharge：兑换是当前页', ...)
it('窄屏：没有 <nav，back=true 时有返回首页', ...)
it('有 Logo 时 img 带加载失败处理', ...)          // 读 UserTopNav.vue 源文件含 @error
it('FaqList：id="help" 一次，7 个 <details', ...)
it('UserTwoColumn：没传 aside 就没有 <aside；wide 在两栏之后', ...)
it('initLayoutMode：matches 真→true，change 事件→false，没有 matchMedia 不抛错', ...)
it('脚本和样式用同一个断点', () => {
  // DESKTOP_QUERY === '(min-width: 1024px)'；user-theme.css 和 UserTwoColumn.vue 源文件里的媒体查询都是 (min-width: 1024px)
})
it('中英文都有 redeemUi.nav.redeem 和 redeemUi.nav.label', ...)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/redeem/__tests__/layout.spec.ts`
Expected: FAIL（文件不存在）

- [ ] **Step 3: 实现** 上述四个文件和改动；`HomeView.vue` 本任务不动（任务 7 改用 `FaqList`）。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS（`visual-header-tools.test.ts` 若断言页头源文件结构，按新结构改，断言的意图不变）

- [ ] **Step 5: 提交**

```bash
git add frontend/src/lib/layoutMode.ts frontend/src/components/redeem/FaqList.vue frontend/src/components/redeem/UserTwoColumn.vue frontend/src/components/redeem/UserTopNav.vue frontend/src/components/UserPageHeader.vue frontend/src/main.ts frontend/src/styles/user-theme.css frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts frontend/src/components/redeem/__tests__/layout.spec.ts <改了的 visual 测试>
git commit -m "feat(user-ui): 电脑布局零件：导航条、两栏、共用常见问题"
```

---

### Task 7: 首页和兑换页用两栏和步骤条

**Files:**
- Modify: `frontend/src/views/HomeView.vue`、`frontend/src/views/user/RechargeView.vue`、`frontend/src/components/redeem/CredentialStep.vue`
- Test: `frontend/src/views/__tests__/home.spec.ts`、`frontend/src/views/__tests__/recharge.spec.ts`、`frontend/src/components/redeem/__tests__/credential.spec.ts`

**Interfaces:**
- Consumes: 任务 6 的 `isDesktop`、`FaqList`、`UserTwoColumn`、`UserPageHeader` 的 `home` 参数。
- Produces: `CredentialStep` 新参数 `guideInAside?: boolean`（默认 false），为真时不渲染 `DeviceGuide`，其余不变。

- [ ] **Step 1: 写失败的测试**
  - `home.spec.ts`：
    - 电脑布局：含 `data-testid="progress-ticks"`，第 1 格带 `aria-current="step"`；`id="help"` 出现 1 次且在 `<aside` 里；
    - 窄屏：没有 `progress-ticks`；`id="help"` 出现 1 次，且在 `data-testid="code-where"` 之后；
    - 原有用例照旧通过。
  - `credential.spec.ts`：`guideInAside` 为真时没有 `data-testid="device-guide"`，为假时有。
  - `recharge.spec.ts`：
    - 照现有用例把流程放到登录信息这一屏：电脑布局时 `device-guide` 在 `<aside` 里，而且只出现一次；窄屏时不在 `<aside` 里；
    - 其他屏电脑布局时右栏有常见问题，窄屏时没有 `id="help"`；
    - 结构断言（读 `RechargeView.vue` 源文件）：`<CredentialStep` 上没有随 `isDesktop` 变化的 `:key`，也不在以 `isDesktop` 为条件的 `v-if` 分支里（Review Focus 1）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/__tests__/home.spec.ts src/views/__tests__/recharge.spec.ts src/components/redeem/__tests__/credential.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现**
  - `HomeView.vue`：`<main class="u-page u-page--split home">`；电脑布局时在两栏上方放 `<ProgressTicks :code-label="null" :current="1" :done="false" />`；`UserTwoColumn` 主栏放现有的 `CodeEntry` 和状态卡或「卡密在哪」卡，`aside` 放 `FaqList`（窄屏时它自然排在下面，和现在顺序一样）；删掉页内原来的常见问题标记和样式；`UserFooter` 留在两栏之后；`UserPageHeader` 传 `home`。
  - `RechargeView.vue`：`<main class="u-page u-page--split recharge">`；`ProgressTicks` 照旧在最上面；当前屏放进 `UserTwoColumn` 主栏；`aside`：登录信息这一屏且电脑布局时放 `<DeviceGuide :initial="device" />`（`device` 在 `onMounted` 里用 `detectDevice(navigator.userAgent, navigator.maxTouchPoints)` 算，和 `CredentialStep` 一样），其他屏电脑布局时放 `FaqList`，窄屏不放；`CredentialStep` 传 `:guide-in-aside="isDesktop"`。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/views/HomeView.vue frontend/src/views/user/RechargeView.vue frontend/src/components/redeem/CredentialStep.vue frontend/src/views/__tests__/home.spec.ts frontend/src/views/__tests__/recharge.spec.ts frontend/src/components/redeem/__tests__/credential.spec.ts)
git add "${F[@]}" && git commit -m "feat(user-ui): 首页和兑换页在电脑上改为两栏，加步骤条"
```

---

### Task 8: 批量兑换、账单查询、卡密查询、代理换码用两栏

**Files:**
- Modify: `frontend/src/views/user/BatchRedeemView.vue`、`BillingCheckView.vue`、`CDKStatusView.vue`、`AgentSwapView.vue`
- Test: Create `frontend/src/views/__tests__/user-pages-layout.spec.ts`；按需改 `visual-batch-vars.test.ts`、`visual-lookup-table.test.ts`、`billing-agent-i18n.spec.ts`

**Interfaces:**
- Consumes: 任务 6 的 `isDesktop`、`FaqList`、`UserTwoColumn`。

- [ ] **Step 1: 写失败的测试** `user-pages-layout.spec.ts`，四个页面各两例：
  - 电脑布局：`id="help"` 出现 1 次且在 `<aside` 里；页面原有的表单区还在（各页取一个现有的 `data-testid` 或标题文字断言）；
  - 窄屏：没有 `id="help"`；
  - 另加 `CDKStatusView` 源文件断言：批量结果放在 `#wide` 插槽里。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/views/__tests__/user-pages-layout.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现**：四页 `main` 加 `u-page--split`；现有内容整体放进 `UserTwoColumn` 主栏（输入框旁的提示留在原位）；`<template v-if="isDesktop" #aside><FaqList /></template>`；`UserFooter` 留在两栏之后。`CDKStatusView` 的批量结果移到 `#wide`，电脑布局下不再用 `lk-wide` 的 960px 限宽（窄屏保持现状）。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/views/user/BatchRedeemView.vue frontend/src/views/user/BillingCheckView.vue frontend/src/views/user/CDKStatusView.vue frontend/src/views/user/AgentSwapView.vue frontend/src/views/__tests__/user-pages-layout.spec.ts)
git add "${F[@]}" <改了的 visual 测试> && git commit -m "feat(user-ui): 批量兑换、账单查询、卡密查询、代理换码在电脑上改为两栏"
```

---

### Task 9: 侧栏外壳 SideNavShell，管理后台改用它

**Files:**
- Create: `frontend/src/components/shell/SideNavShell.vue`
- Modify: `frontend/src/layouts/AdminLayout.vue`、`frontend/src/lib/adminNav.ts`
- Test: `frontend/src/layouts/__tests__/AdminLayout.spec.ts`

**Interfaces:**
- Consumes: 任务 2 写在根元素上的 `--side-bg`、`--side-ink`、`--side-muted`、`--side-active`、`--side-active-on`。
- Produces:
  - `adminNav.ts`：`readNavPref(key = NAV_STORE_KEY): NavPref`、`writeNavPref(v: 'expanded' | 'collapsed', key = NAV_STORE_KEY): void`（不传 key 时行为不变）。
  - `SideNavShell.vue` 参数：`groups: NavGroup[]`、`isActive: (path: string) => boolean`、`brandName: string`、`logoSrc: string`（空串表示没有 Logo）、`subLabel: string`、`navLabel: string`（侧栏的 `aria-label`）、`navId: string`、`prefKey: string`、`homePath: string`。插槽：`topbar`（顶栏里汉堡按钮右边的内容）、`foot`（侧栏底部、折叠按钮之外的内容）、默认插槽（页面）。折叠、抽屉、窗口宽度、Esc 关闭、路由变化关抽屉、打开抽屉时聚焦第一个菜单项，这些逻辑原样从 `AdminLayout.vue` 搬进来，存取偏好用 `prefKey`。
  - 侧栏样式：底色 `var(--side-bg)`，菜单字 `var(--side-ink)`，分组标题 `var(--side-muted)`；选中项 `class="sn-item active"`，底 `var(--side-active)`、字 `var(--side-active-on)`、`font-weight: 700`、`box-shadow: inset 3px 0 0 #FFFFFF`；顶部有 Logo 时 `<span class="sn-logo-tile">`（白底、圆角 8px）包住 `<img>`，`@error` 后隐藏；去掉菱形 `<svg class="sn-logo">`；站名过长省略号截断；顶栏 `var(--surface-solid)` 底、`var(--brd)` 下边线。
- `AdminLayout.vue` 只保留：菜单数据、`isActive`、面包屑和环境标记（放 `topbar`）、`ThemeToggle` 和账号菜单（放 `foot`）、修改密码弹窗、功能开关跳转、页面标题。`navId` 仍是 `admin-sidenav`，`prefKey` 仍是 `ops-sidenav`。

- [ ] **Step 1: 写失败的测试**（`AdminLayout.spec.ts`，原有用例全部保留）：

```ts
it('选中项带 active 类，侧栏用侧栏变量', ...)      // HTML 里当前页的 sn-item 有 active；读 SideNavShell.vue 源文件含 var(--side-bg) 和 inset 3px 0 0 #FFFFFF
it('有 Logo：白底小块里显示图片，带加载失败处理', ...)   // siteBrand.logoVersion='abc' → 含 sn-logo-tile 和 /api/v1/public/site/logo?v=abc；源文件 img 带 @error
it('没 Logo：不显示菱形图标，只显示站名', ...)      // 不含 sn-logo-tile、不含 class="sn-logo"
it('readNavPref/writeNavPref 可以换键，不传键时仍用 ops-sidenav', ...)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/layouts/__tests__/AdminLayout.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现** `SideNavShell.vue`，`AdminLayout.vue` 改用它，`adminNav.ts` 加 key 参数。

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS（原有 11 项菜单、3 个分组、高亮、面包屑、折叠偏好用例都过）

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/components/shell/SideNavShell.vue frontend/src/layouts/AdminLayout.vue frontend/src/lib/adminNav.ts frontend/src/layouts/__tests__/AdminLayout.spec.ts)
git add "${F[@]}" && git commit -m "feat(admin): 抽出侧栏外壳，侧栏颜色由皮肤主色推出，选中项加粗加竖线"
```

---

### Task 10: 代理商后台改用侧栏外壳

**Files:**
- Modify: `frontend/src/layouts/AgentLayout.vue`、`frontend/src/element.ts`（只在下面的图标没注册时补注册）
- Test: `frontend/src/views/__tests__/agent.spec.ts`（或新建 `frontend/src/layouts/__tests__/AgentLayout.spec.ts`）

**Interfaces:**
- Consumes: 任务 9 的 `SideNavShell`。
- 菜单（顺序、路径、文字、是否精确匹配不变）和图标：概览 `/agent` 精确 `Odometer`；下单 `/agent/order` `ShoppingCart`；我的码 `/agent/codes` `Tickets`；订单 `/agent/orders` `List`；流水 `/agent/ledger` `Wallet`；对接异次元 `/agent/integration` `Connection`；账号 `/agent/account` `User`。放在一个无名分组里。
- `navId 'agent-sidenav'`、`prefKey 'agent-sidenav'`、`subLabel '代理商后台'`、`navLabel '代理商导航'`、`homePath '/agent'`、站名用 `siteBrand.name`、Logo 同管理后台。
- `foot` 插槽：代理商名称（`display_name || username`）、`余额 ${fmtUsd(balance_cents)}`、`ThemeToggle`、「退出登录」按钮。`topbar` 插槽：当前菜单项的文字。
- 退出确认、未授权跳登录、功能关闭跳登录、`loadMe` 的逻辑原样保留。

- [ ] **Step 1: 写失败的测试**：SSR 渲染 `/agent/orders`：7 个菜单链接按顺序出现，「订单」那项带 `active`；含 `id="agent-sidenav"`、「退出登录」；源文件断言 `prefKey` 是 `agent-sidenav`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/layouts/__tests__/AgentLayout.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/layouts/AgentLayout.vue frontend/src/element.ts frontend/src/layouts/__tests__/AgentLayout.spec.ts)
git add "${F[@]}" && git commit -m "feat(agent): 代理商后台改用侧栏外壳"
```

---

### Task 11: 登录类页面用居中卡片

**Files:**
- Create: `frontend/src/components/AuthCard.vue`
- Modify: `frontend/src/views/auth/LoginView.vue`、`frontend/src/views/auth/RegisterView.vue`、`frontend/src/views/admin/SetupView.vue`、`frontend/src/views/agent/AgentLogin.vue`
- Test: Create `frontend/src/components/__tests__/auth-card.spec.ts`

**Interfaces:**
- Produces: `AuthCard.vue` 参数 `title: string`、`subtitle?: string`、`wide?: boolean`；默认插槽放表单。结构：整屏居中、透明底（透出皮肤底色）；右上角 `ThemeToggle`；卡片 `class="auth-card"`，`var(--surface-solid)` 底、`var(--brd)` 边、`var(--radius-lg)` 圆角，最宽 420px（`wide` 时 520px）；卡片顶部 Logo（有且没加载失败时，`@error` 后隐藏）和站名（过长省略号截断）；然后标题、副标题、插槽。
- `.auth-card` 里的 `.input`：`min-height: 48px`、`border: 3px solid var(--primary)`、`border-radius: var(--radius-md)`、`background: var(--surface-solid)`；`.btn-primary`：`min-height: 48px`、`border-radius: var(--radius-md)`、`background: var(--primary)`、`color: var(--primary-on)`、`background-image: none`、`box-shadow: none`。
- 四个页面去掉自己的外层容器和卡片，内容放进 `AuthCard`；标题文字沿用各页现有的；`SetupView` 用 `wide`。表单字段、校验、出错提示、跳转、返回首页链接不变。

- [ ] **Step 1: 写失败的测试**：
  - `AuthCard` 渲染含 `auth-card` 和站名；`logoVersion` 有值时有 Logo 图片；源文件里 `img` 带 `@error`，并有 `text-overflow: ellipsis`；
  - 四个页面各渲染一次：含 `auth-card`；`LoginView` 仍有用户名和密码两个输入框、「返回首页」链接；`AgentLogin` 在代理商功能关闭时仍只显示提示（沿用现有断言方式）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/components/__tests__/auth-card.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑全部前端测试**

Run: `cd frontend && npx vitest run`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
F=(frontend/src/components/AuthCard.vue frontend/src/views/auth/LoginView.vue frontend/src/views/auth/RegisterView.vue frontend/src/views/admin/SetupView.vue frontend/src/views/agent/AgentLogin.vue frontend/src/components/__tests__/auth-card.spec.ts)
git add "${F[@]}" && git commit -m "feat(auth): 登录类页面改为居中卡片，输入框和按钮用用户端同款"
```

---

### Task 12: 更新说明、门禁和浏览器验收

**Files:**
- Modify: `CHANGELOG.md`（「未发布」一节）
- Modify: 文档里提到「主色」设置位置的地方（`grep -rn "主色" docs README* deploy` 找，规格和计划文件除外）

- [ ] **Step 1: CHANGELOG**：按规格 §10.1 逐条写「升级注意」三块，放在「未发布」最前面；「行为变化」里「几套皮肤的颜色调深或调浅」那条，附上任务 3 报告里的改色表（皮肤、模式、原值 → 新值）。写后台位置前先对照 `frontend/src/router/routes.ts` 和页面标签名。

- [ ] **Step 2: 构建产物检查**

Run: `cd frontend && npm run build && grep -c "fonts.googleapis.com" dist/index.html; ls dist/assets/*.css | xargs grep -l "fonts.googleapis.com"`
Expected: 第一条输出 `0`，第二条没有输出（字体链接只由后台的 `theme.ts` 运行时按需插入）。

- [ ] **Step 3: 本地全量门禁**

Run:
```bash
cd backend && test -z "$(gofmt -l .)" && go vet ./... && golangci-lint run ./... && go test -race ./... && govulncheck ./...
cd ../frontend && npx vitest run && npm run build
cd .. && /Users/zhengshan/go/bin/gitleaks git --log-opts="origin/master..HEAD" --redact --no-banner .
```
Expected: 全部通过；`paybrowser` 包本次没改，若本机偶发失败按 known-issues 处理并在报告里写明。

- [ ] **Step 4: 提交文档**

```bash
git add CHANGELOG.md <改了的文档> && git commit -m "docs(changelog): 子项目 B：皮肤管前后台、深青自助、侧栏外壳、用户端电脑布局"
```

- [ ] **Step 5: 浏览器验收（由主会话做，不派给实现者）**
  - 本机起服务：用 `deploy/app.env.example` 里的变量，`SERVER_PORT` 用 18191，`DB_PATH` 指到会话临时目录里的新文件，`WEB_DIR` 指到本工作区的 `frontend/dist`，`JWT_SECRET`、`APP_MASTER_KEY` 用临时随机值；后台运行，只结束自己启动的进程。走一遍 `/ops/setup` 建管理员。
  - 皮肤看四套：深青自助、曜夜雾金、赛博科技、苹果玻璃；宽度 1440、1024、1023、390；亮色、暗色。
  - 页面：规格 §9.4 的列表；截图存 `/Volumes/ExtSSD/projects/aisubproxy/.playwright-mcp/`。
  - 逐条看 Review Focus 五项：在登录信息这一屏粘贴内容后把窗口从 1440 拉到 900 再拉回来，内容还在；Logo 地址失效时只显示站名；站名设为 30 个汉字时三处都截断、无横向滚动；1024 两栏、1023 单栏；外观页预览别的皮肤后离开，前后台都还原。
  - 看完关掉服务，删掉临时数据库。
