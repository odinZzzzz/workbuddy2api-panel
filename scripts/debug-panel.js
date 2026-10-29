#!/usr/bin/env node
/**
 * 面板前端离线调试器 / 回归断言集。
 *
 * 为什么需要：面板是 go:embed 的单文件前端，改完通常只能"打开浏览器看一眼"验证；
 * 在没有 ego lite / 无 GUI 的环境里这一步做不了，而 Go 侧的顶层冒烟只抓
 * TDZ/ReferenceError，覆盖不到「渲染结果对不对」。这里用 vm + DOM 桩直接调用各视图的
 * render 函数，按**空数据 / 单条 / 异常数据**三态断言：不抛错、不渲染 undefined/NaN、
 * 且确实产出了内容。
 *
 * 用法：
 *   node scripts/debug-panel.js               # 全部视图
 *   node scripts/debug-panel.js --view usage  # 单个视图
 *   node scripts/debug-panel.js --list        # 列出可跑的视图
 *
 * 退出码：0 = 全通过，1 = 有断言失败（可直接被 go test 当作闸门）。
 */
const fs = require('fs')
const path = require('path')
const vm = require('vm')

const ROOT = path.resolve(__dirname, '..')
const APP = path.join(ROOT, 'internal/panel/app.js')
const src = fs.readFileSync(APP, 'utf8')

/* ── 最小 DOM 桩：记录每个元素的 innerHTML/textContent，够 render 用 ── */
const elements = new Map()
function makeEl(id) {
  const el = {
    id,
    _html: '',
    textContent: '',
    value: '',
    hidden: false,
    disabled: false,
    dataset: {},
    style: {},
    children: [],
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    get innerHTML() { return this._html },
    set innerHTML(v) { this._html = String(v) },
    appendChild() {},
    remove() {},
    addEventListener() {},
    removeEventListener() {},
    // querySelector 返回一个桩而不是 null：不少 render 里会
    // `empty.querySelector('.t').textContent = ...`，返回 null 会误报崩溃。
    querySelector: () => makeEl('qs'),
    querySelectorAll: () => [],
    closest: () => null,
    focus() {},
    select() {},
    click() {},
    insertAdjacentHTML() {},
  }
  return el
}
function $(sel) {
  if (typeof sel !== 'string') return makeEl('anon')
  const id = sel.startsWith('#') ? sel.slice(1) : sel
  if (!elements.has(id)) elements.set(id, makeEl(id))
  return elements.get(id)
}

const sandbox = {
  console,
  setTimeout: () => 0,
  setInterval: () => 0,
  clearTimeout: () => {},
  clearInterval: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
  location: { hash: '', href: '', reload() {} },
  history: { replaceState() {} },
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  navigator: { clipboard: { writeText: () => Promise.resolve() } },
  document: {
    querySelector: $,
    querySelectorAll: () => [],
    getElementById: (id) => $(id),
    addEventListener() {},
    createElement: () => makeEl('created'),
    documentElement: makeEl('html'),
    head: makeEl('head'),
    body: makeEl('body'),
    cookie: '',
  },
  fetch: () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) }),
  alert: () => {},
  confirm: () => true,
  Date, Math, JSON, Number, String, Object, Array, RegExp, Promise,
  parseInt, parseFloat, isNaN, encodeURIComponent, decodeURIComponent,
}
sandbox.window = sandbox
sandbox.globalThis = sandbox

const ctx = vm.createContext(sandbox)
try {
  vm.runInContext(src, ctx, { filename: 'app.js' })
} catch (e) {
  console.error('❌ app.js 求值失败：', (e && e.stack) || e)
  process.exit(1)
}

/* ── 断言工具 ── */
let failed = 0
let passed = 0
function check(name, cond, detail) {
  if (cond) { passed++ }
  else { failed++; console.log('  ❌ ' + name + (detail ? '\n     ' + detail : '')) }
}
function call(fn, ...args) {
  if (typeof ctx[fn] !== 'function') throw new Error(`app.js 里没有函数 ${fn}（被改名/删除了？）`)
  return ctx[fn](...args)
}

// 快照所有已知元素内容，用于判断"这次 render 到底写了没有"
const WATCH_IDS = [
  'accBody', 'mlBody', 'mlNote', 'reqSummary', 'reqBody', 'usStats', 'usChart',
  'pkSummary', 'pkList', 'expList', 'expNote', 'expBox', 'qcList', 'qcSummary', 'tcEmpty',
  'tokBody', 'tokCount', 'dashKpis', 'dashChart', 'dashBalance', 'dashInFlight', 'dashNote',
]
function snapshot() {
  const out = {}
  for (const id of WATCH_IDS) {
    const el = elements.get(id)
    out[id] = el ? (el._html || el.textContent || '') : ''
  }
  return out
}
function changed(a, b) {
  return WATCH_IDS.some((id) => (a[id] || '') !== (b[id] || ''))
}
function badText(snap) {
  return Object.entries(snap).find(([, v]) => /undefined|NaN|\[object Object\]/.test(v || ''))
}

/* 三态断言：对同一 render 依次喂 空 / 单条 / 异常 数据 */
function expectRender(label, fn, cases) {
  console.log('\n[' + label + '] ' + fn)
  for (const c of cases) {
    const before = snapshot()
    let threw = null
    try { call(fn, ...c.args) } catch (e) { threw = e }
    const after = snapshot()
    check(`${label}/${c.name}: 不抛错`, !threw, threw ? String((threw && threw.stack) || threw) : '')
    if (threw) continue
    const bad = badText(after)
    check(`${label}/${c.name}: 无 undefined/NaN/[object Object]`, !bad, bad ? `${bad[0]} = ${String(bad[1]).slice(0, 160)}` : '')
    check(`${label}/${c.name}: 确实写了内容`, changed(before, after) || Object.values(after).some((v) => v && v.length), JSON.stringify(after).slice(0, 200))
  }
}

/* ── 各视图用例 ── */
const ACCOUNT = {
  uid: 'u1', nickname: '芥末', realm: 'cn', credits: 4209, credits_total: 5000,
  healthy: true, cooling: false, disabled: false, paused: false, in_flight: 1,
  success_count: 120, err_total: 3, last_success: '2026-10-30T01:00:00+08:00',
  credits_expiring: 439, credits_earliest_expiry: '2026-10-31T23:59:59+08:00',
  credits_earliest_remaining: 439, token_usage: { request_count: 10, total_tokens: 12345, last_latency_ms: 800 },
}
const BROKEN_ACCOUNT = { uid: 'u2' } // 只有 uid：缺昵称/余额/时间

const VIEWS = {
  accounts: () => expectRender('账号池', 'renderAccounts', [
    { name: '空数据', args: [[]] },
    { name: '单条', args: [[ACCOUNT]] },
    { name: '异常数据', args: [[BROKEN_ACCOUNT, null]] },
  ]),

  modelLocks: () => expectRender('模型锁池', 'renderModelLocks', [
    { name: '空数据', args: [null] },
    { name: '单条', args: [[{ model: 'deepseek-v4.1-flash', realm: 'cn', state: 'locked', servable: 0, total: 2, locked: 2, unlock_at: '2026-10-30T10:40:00+08:00', reason: '6004 model rate limit' }]] },
    { name: '异常数据', args: [[{ model: 'x' }, null]] },
  ]),

  requestMetrics: () => expectRender('请求指标', 'renderRequestMetrics', [
    { name: '空数据', args: [{}, []] },
    { name: '单条', args: [{ completed: 10, succeeded: 10, failed: 0, success_rate: 100, http_success_rate: 100, avg_duration_ms: 1200, in_flight: 2, archive: { enabled: true, files: 3, bytes: 1000 } }, []] },
    { name: '异常数据', args: [{ completed: null, success_rate: null }, null] },
  ]),

  usage: () => expectRender('用量', 'renderUsage', [
    { name: '空数据', args: [{}] },
    {
      name: '单条',
      args: [{
        totals: { requests: 10, errors: 1, prompt_tokens: 1000, completion_tokens: 200, total_tokens: 1200, credits: 3.5, avg_latency_ms: 900 },
        by_account: [], by_model: [], series: [{ t: '2026-10-30T01', requests: 5, prompt_tokens: 500, completion_tokens: 100, total_tokens: 600, credits: 1.2 }],
        credit_by_account: [], credit_by_model: [], buckets: 1,
      }],
    },
    { name: '异常数据', args: [{ totals: { requests: null, total_tokens: undefined }, series: null }] },
  ]),

  packages: () => expectRender('积分构成', 'renderPackages', [
    { name: '空数据', args: [{}, 5] },
    {
      name: '单条',
      args: [{
        accounts: [{
          uid: 'u1', nickname: '芥末', credits: 4209, credits_total: 5000,
          packages: [{ size: 1000, remaining: 400, end_time: '2026-10-31 23:59:59' }],
        }],
      }, 5],
    },
    { name: '异常数据', args: [{ accounts: [{ uid: 'u2', packages: null }] }, 5] },
  ]),

  expiry: () => expectRender('到期提醒', 'renderExpirySnapshot', [
    { name: '空数据', args: [{}] },
    { name: '单条', args: [{ accounts: [ACCOUNT] }] },
    { name: '异常数据', args: [{ accounts: [{ uid: 'u3', credits_earliest_expiry: 'not-a-date' }] }] },
  ]),

  tokens: () => expectRender('令牌', 'renderTokens', [
    { name: '空数据', args: [[], false] },
    {
      name: '单条',
      args: [[{
        id: 't1', name: '客户端A', key: 'sk-abcd…wxyz', enabled: true, expired: false, quota_exceeded: false,
        quota_credits: 100, used_credits: 42.5, used_requests: 12, expires_at: '2026-11-01T00:00:00+08:00',
        allowed_models: ['cn:deepseek-v4-flash'], rpm: 60, max_concurrency: 4, in_flight: 1,
      }], false],
    },
    { name: '异常数据', args: [[{ id: 't2' }], false] },
    { name: '未启用', args: [[], true] },
  ]),

  dashboard: () => {
    const ov = { healthy: 5, total: 8, cooling: 1, disabled: 0, in_flight_full: 0, accounts: [ACCOUNT, BROKEN_ACCOUNT] }
    const mt = {
      in_flight: 2, completed: 10, failed: 0, success_rate: 100, http_success_rate: 100, avg_duration_ms: 11080,
      recent: [{ time: new Date().toISOString(), credit: 1.5, total_tokens: 1200 }],
    }
    const us = { totals: { credits: 144.21, requests: 239 } }
    const bal = { enabled: true, points: [{ t: new Date().toISOString(), a: { u1: 4209 }, total: 4209 }], accounts: [{ uid: 'u1', nickname: '芥末' }] }
    expectRender('仪表盘', 'renderDashboard', [
      { name: '空数据', args: [{}, {}, {}, { enabled: false }] },
      { name: '单条', args: [ov, mt, us, bal] },
      { name: '异常数据', args: [{ accounts: null }, { recent: null }, { totals: null }, { points: null }] },
    ])
    // 余额图的三种空态单独断言（都是 UI 分支，容易漏）
    call('renderBalanceChart', { enabled: true, points: [], accounts: [] })
    check('仪表盘/余额图 无采样点空态', ($('dashBalance').innerHTML || '').includes('还没有采样点'))
    call('renderBalanceChart', { enabled: false })
    check('仪表盘/余额图 未启用空态', ($('dashBalance').innerHTML || '').includes('未启用'))
  },

  queue: () => expectRender('任务队列', 'renderQueue', [
    { name: '空数据', args: [[], {}, '没有任务', '先扫描'] },
    {
      name: '单条',
      args: [[{ uid: 'u1', nick: '芥末', rows: [{ code: 'chat_5', status: 'pending', prog: '3/5' }] }], { done: 0, total: 1, items: [] }, '', ''],
    },
  ]),
}

/* ── 运行 ── */
const argView = (() => {
  const i = process.argv.indexOf('--view')
  return i > 0 ? process.argv[i + 1] : 'all'
})()
if (process.argv.includes('--list')) {
  console.log(Object.keys(VIEWS).join('\n'))
  process.exit(0)
}
if (argView !== 'all' && !VIEWS[argView]) {
  console.error(`未知视图 ${argView}；可用：${Object.keys(VIEWS).join(', ')}`)
  process.exit(1)
}

for (const [name, run] of Object.entries(VIEWS)) {
  if (argView !== 'all' && argView !== name) continue
  try { run() } catch (e) {
    failed++
    console.log(`\n[${name}] ❌ 用例本身抛错：${(e && e.stack) || e}`)
  }
}

console.log(`\n结果：PASS=${passed} FAIL=${failed}`)
process.exit(failed === 0 ? 0 : 1)
