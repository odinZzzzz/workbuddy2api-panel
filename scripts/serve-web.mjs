#!/usr/bin/env node
/**
 * 前后端分离开发用的独立前端静态服务器（零依赖，只用 Node 标准库）。
 *
 * 为什么需要：面板前端（index.html / app.js / echarts.min.js）原本经 go:embed
 * 与 Go 二进制同体部署，改前端必须重新编译。分离后前端可独立跑在一个端口，
 * 后端只提供 /panel/api/*，两边独立重启、独立部署。
 *
 * 用法：
 *   node scripts/serve-web.mjs                       # 默认 :5173，API 指向 :7863
 *   node scripts/serve-web.mjs --port 5173 --api http://127.0.0.1:7863
 *   node scripts/serve-web.mjs --api https://api.example.com   # 跨域真机调试
 *
 * 行为：
 *   - 静态根默认 internal/panel（前端源码唯一副本，不做复制，避免双份维护）
 *   - 返回 index.html 时注入 window.WB2A_API_BASE，app.js 由此决定 fetch 目标
 *   - 不做任何代理：跨域由后端 panel.cors_origins / WB2A_CORS_ORIGINS 放行，
 *     因此这里能验证真实的跨域路径，而不是被代理掩盖
 */
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const args = process.argv.slice(2)
const argOf = (name, fallback) => {
  const i = args.indexOf('--' + name)
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback
}

const PORT = Number(argOf('port', process.env.WEB_PORT || '5173'))
const HOST = argOf('host', process.env.WEB_HOST || '127.0.0.1')
const API_BASE = argOf('api', process.env.WB2A_API_BASE || 'http://127.0.0.1:7863')
const WEB_DIR = path.resolve(ROOT, argOf('dir', 'internal/panel'))

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://' + HOST)
  let rel = decodeURIComponent(url.pathname)
  // /panel 是面板的挂载前缀：剥掉它再映射到静态根，这样页面里的相对引用
  // （app.js / echarts.min.js）在独立服务下与 Go 同源部署的路径一致。
  if (rel === '/' || rel === '/panel' || rel === '/panel/') rel = '/index.html'
  else if (rel.startsWith('/panel/')) rel = rel.slice('/panel'.length)
  // 只允许 web 根下的普通文件，挡住 ../ 穿越
  const file = path.resolve(WEB_DIR, '.' + rel)
  if (!file.startsWith(WEB_DIR + path.sep) && file !== WEB_DIR) {
    res.writeHead(403).end('forbidden')
    return
  }
  fs.readFile(file, (err, buf) => {
    if (err) {
      res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('not found: ' + rel)
      return
    }
    const ext = path.extname(file).toLowerCase()
    if (ext === '.html') {
      // 注入 API 地址：这是分离部署唯一的“接线点”，比改源码更安全
      const inject = '<script>window.WB2A_API_BASE=' + JSON.stringify(API_BASE.replace(/\/+$/, '') + '/panel/api') + ';<\/script>'
      const html = buf.toString('utf8').replace('</head>', inject + '\n</head>')
      res.writeHead(200, { 'Content-Type': MIME['.html'], 'Cache-Control': 'no-store' }).end(html)
      return
    }
    res.writeHead(200, {
      'Content-Type': MIME[ext] || 'application/octet-stream',
      'Cache-Control': ext === '.js' ? 'no-store' : 'public, max-age=300',
    }).end(buf)
  })
})

server.listen(PORT, HOST, () => {
  console.log('前端静态服务: http://' + HOST + ':' + PORT + '/panel/')
  console.log('静态根      : ' + WEB_DIR)
  console.log('API 目标    : ' + API_BASE + '/panel/api')
  console.log('提示        : 后端需放行该来源，例如 WB2A_CORS_ORIGINS=http://' + HOST + ':' + PORT)
})
