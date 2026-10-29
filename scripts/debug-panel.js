#!/usr/bin/env node
/**
 * 面板前端离线调试器：不装浏览器也能跑 app.js 里的 render/工具函数。
 *
 * 为什么需要：面板是 go:embed 的单文件前端，改完只能靠"打开浏览器看一眼"验证——
 * 而在没有 ego lite / 无 GUI 的机器上（CI、服务器、本机工具缺失时）这一步做不了，
 * 只能等 Go 测试里的顶层冒烟去抓 TDZ 类错误，覆盖不到"渲染结果对不对"。
 *
 * 用法：
 *   node scripts/debug-panel.js
 *   node scripts/debug-panel.js --view tokens      # 只看某个视图
 *
 * 做法：用 vm 在一个"够用的 DOM 桩"里执行 app.js，然后**直接调用**目标渲染函数，
 * 把产出的 HTML 拿去断言。app.js 顶层会 start()/setInterval，这里把定时器打桩成
 * 空操作，避免脚本自己跑起来。
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
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    style: {},
    children: [],
    get innerHTML() { return this._html },
    set innerHTML(v) { this._html = String(v) },
    appendChild() {},
    remove() {},
    addEventListener() {},
    removeEventListener() {},
    querySelector: () => null,
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
  // 定时器打桩：不让 app.js 顶层自己跑起来
  setTimeout: () => 0,
  setInterval: () => 0,
  clearTimeout: () => {},
  clearInterval: () => {},
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
  // app.js 顶层用裸 addEventListener（浏览器里是 window 上的）
  addEventListener: () => {},
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
  removeEventListener: () => {},
  fetch: () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) }),
  alert: () => {},
  confirm: () => true,
  Date, Math, JSON, Number, String, Object, Array, RegExp, Promise, parseInt, parseFloat, isNaN, encodeURIComponent, decodeURIComponent,
}
sandbox.window = sandbox
sandbox.globalThis = sandbox

const ctx = vm.createContext(sandbox)
try {
  vm.runInContext(src, ctx, { filename: 'app.js' })
} catch (e) {
  console.error('❌ app.js 求值失败：', e && e.stack || e)
  process.exit(1)
}

/* ── 断言工具 ── */
let failed = 0
function check(name, cond, detail) {
  if (cond) { console.log('  ✅ ' + name) }
  else { failed++; console.log('  ❌ ' + name + (detail ? '\n     ' + detail : '')) }
}
function call(fn, ...args) {
  if (typeof ctx[fn] !== 'function') throw new Error(`app.js 里没有函数 ${fn}（被改名/删除了？）`)
  return ctx[fn](...args)
}

const only = (() => {
  const i = process.argv.indexOf('--view')
  return i > 0 ? process.argv[i + 1] : 'all'
})()

/* ── 令牌视图 ── */
if (only === 'all' || only === 'tokens') {
  console.log('\n[令牌视图] renderTokens / tokStatus / tokQuotaCell')
  const sample = [
    { id: 't1', name: '客户端A', key: 'sk-abcdef…wxyz', enabled: true, expired: false, quota_exceeded: false,
      quota_credits: 100, used_credits: 42.5, used_requests: 12, expires_at: '2026-11-01T00:00:00+08:00',
      allowed_models: ['cn:deepseek-v4-flash'], note: '测试' },
    { id: 't2', name: '已禁用', key: 'sk-bbbbbb…cccc', enabled: false, expired: false, quota_exceeded: false,
      quota_credits: 0, used_credits: 0, used_requests: 0 },
    { id: 't3', name: '过期', key: 'sk-dddddd…eeee', enabled: true, expired: true, quota_exceeded: false,
      quota_credits: 0, used_credits: 1, used_requests: 1 },
    { id: 't4', name: '配额满', key: 'sk-ffffff…gggg', enabled: true, expired: false, quota_exceeded: true,
      quota_credits: 10, used_credits: 10, used_requests: 3 },
  ]
  call('renderTokens', sample, false)
  const html = $('tokBody').innerHTML
  check('渲染出 4 行', (html.match(/<tr>/g) || []).length === 4, '实际: ' + (html.match(/<tr>/g) || []).length)
  check('可用状态标签', html.includes('可用'))
  check('禁用状态标签', html.includes('已禁用'))
  check('过期状态标签', html.includes('已过期'))
  check('配额用尽标签', html.includes('配额用尽'))
  check('配额百分比', html.includes('43%'), html.slice(0, 200))
  check('不限配额显示', html.includes('不限'))
  check('操作按钮齐全', html.includes('data-tok="toggle"') && html.includes('data-tok="reset"') && html.includes('data-tok="del"'))
  check('白名单提示', html.includes('仅 cn:deepseek-v4-flash'))

  call('renderTokens', [], false)
  check('空列表有引导文案', $('tokBody').innerHTML.includes('还没有令牌'))

  call('renderTokens', [], true)
  check('未启用令牌库有提示', $('tokBody').innerHTML.includes('未启用令牌管理'))
}

/* ── 仪表盘 ── */
if (only === 'all' || only === 'dashboard') {
  console.log('\n[仪表盘] renderDashboard')
  const nowIso = new Date().toISOString()
  const ov = { healthy: 5, total: 8, cooling: 1, disabled: 0, in_flight_full: 0,
    accounts: [{ uid: 'u1', nickname: '芥末', in_flight: 2, credits: 4209 },
               { uid: 'u2', nickname: 'k***@gmail.com', in_flight: 0, credits: 440 }] }
  const mt = { in_flight: 2, completed: 10, failed: 0, success_rate: 100, http_success_rate: 100,
    avg_duration_ms: 11080,
    recent: [{ time: nowIso, credit: 1.5, total_tokens: 1200 },
             { time: nowIso, credit: 2.5, total_tokens: 800 }] }
  const us = { totals: { credits: 144.21, requests: 239 } }
  call('renderDashboard', ov, mt, us)
  const kpi = $('dashKpis').innerHTML
  check('KPI 含在途请求', kpi.includes('在途请求'))
  check('KPI 含近 5 分钟积分', kpi.includes('近 5 分钟积分'))
  check('KPI 含今日消耗', kpi.includes('今日消耗积分'))
  check('近 5 分钟积分=4.00', kpi.includes('4.00'), kpi.slice(0, 300))
  const chart = $('dashChart').innerHTML
  check('图表出 SVG', chart.startsWith('<svg'))
  check('图表有柱子', chart.includes('<rect'))
  const rows = $('dashInFlight').innerHTML
  check('在途分布两行', (rows.match(/ifrow/g) || []).length === 2)
  check('在途数正确', rows.includes('2 在途'))

  // 余额趋势：有采样点 → 统计行；无采样点/未启用 → 对应空态
  const bal = {
    enabled: true,
    points: [
      { t: '2026-10-30T01:00:00+08:00', a: { u1: 4000, u2: 400 }, total: 4400 },
      { t: '2026-10-30T01:05:00+08:00', a: { u1: 3950, u2: 380 }, total: 4330 },
    ],
    accounts: [{ uid: 'u1', nickname: '芥末' }, { uid: 'u2', nickname: 'k***@gmail.com' }],
  }
  call('renderDashboard', ov, mt, us, bal)
  check('余额趋势统计行', ($('dashBalNote').textContent || '').includes('2 个采样点'), $('dashBalNote').textContent)
  check('余额趋势当前合计', ($('dashBalNote').textContent || '').includes('4330'), $('dashBalNote').textContent)
  call('renderDashboard', ov, mt, us, { enabled: true, points: [], accounts: [] })
  check('无采样点空态', $('dashBalance').innerHTML.includes('还没有采样点'))
  call('renderDashboard', ov, mt, us, { enabled: false })
  check('未启用空态', $('dashBalance').innerHTML.includes('未启用'))
}

console.log(failed === 0 ? '\n全部通过' : `\n${failed} 项失败`)
process.exit(failed === 0 ? 0 : 1)
