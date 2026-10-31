package qqbot

// admin_page.go —— 后台页面（单文件、无外链、无构建步骤）。
//
// 为什么手写一页而不是引前端框架：这个后台只有几个表单和两张表，服务端在 1.7G 的
// 小机器上，任何构建链和运行时都是净负担。整页 ~7KB，浏览器打开即用。
//
// 交互：GET /admin/api/state 拉数据 → 渲染；保存走 POST /admin/api/config（热生效）。
// token 通过 ?token= 传入后存进 sessionStorage，后续请求自动带 Authorization。
const adminHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>QQ Bot 后台</title>
<style>
  :root{--bg:#0f1115;--card:#171a21;--line:#262b36;--ink:#e6e9ef;--ink2:#9aa3b2;--acc:#5b7cfa;--ok:#3ddc97;--warn:#f5b544;--err:#e06c6c}
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 -apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif}
  header{padding:14px 18px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:12px}
  h1{font-size:16px;margin:0;font-weight:600}
  .pill{font-size:12px;padding:2px 8px;border-radius:999px;background:#1e2430;color:var(--ink2)}
  main{max-width:1080px;margin:0 auto;padding:18px}
  .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:12px}
  .card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:14px}
  .card h2{font-size:13px;margin:0 0 10px;color:var(--ink2);font-weight:600;letter-spacing:.02em}
  .kpi{font-size:22px;font-weight:600}
  .kpi small{font-size:12px;color:var(--ink2);font-weight:400}
  label{display:block;font-size:12px;color:var(--ink2);margin:10px 0 4px}
  input[type=text],input[type=number],textarea,select{width:100%;background:#11141a;border:1px solid var(--line);border-radius:8px;color:var(--ink);padding:8px 10px;font:13px/1.5 inherit}
  textarea{min-height:180px;resize:vertical}
  button{background:var(--acc);color:#fff;border:0;border-radius:8px;padding:8px 14px;font-size:13px;cursor:pointer}
  button.ghost{background:#222836;color:var(--ink)}
  button:disabled{opacity:.5;cursor:default}
  table{width:100%;border-collapse:collapse;font-size:13px}
  th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}
  th{color:var(--ink2);font-weight:500;font-size:12px}
  .row{display:flex;gap:10px;align-items:center;margin-top:12px}
  .muted{color:var(--ink2)}
  .ok{color:var(--ok)}.warn{color:var(--warn)}.err{color:var(--err)}
  code{background:#11141a;padding:1px 5px;border-radius:4px;font-size:12px}
  #toast{position:fixed;right:16px;bottom:16px;background:#1e2430;border:1px solid var(--line);border-radius:8px;padding:10px 14px;opacity:0;transition:.2s}
  #toast.show{opacity:1}
</style>
</head>
<body>
<header>
  <h1>QQ Bot 后台</h1>
  <span class="pill" id="pillEnabled">—</span>
  <span class="pill" id="pillModel">—</span>
  <span class="pill" id="pillUptime">—</span>
</header>
<main>
  <div class="grid">
    <div class="card"><h2>今日事件</h2><div class="kpi" id="kEvents">—</div></div>
    <div class="card"><h2>已回复</h2><div class="kpi ok" id="kReplied">—</div></div>
    <div class="card"><h2>忽略/静默</h2><div class="kpi muted" id="kSkipped">—</div></div>
    <div class="card"><h2>记住的用户</h2><div class="kpi" id="kMem">—</div></div>
  </div>

  <div class="card" style="margin-top:12px">
    <h2>人设（system prompt）</h2>
    <textarea id="persona" placeholder="龙王人设…"></textarea>
    <div class="grid" style="margin-top:10px">
      <div><label>模型</label><input type="text" id="model" placeholder="cn:deepseek-v4-flash"></div>
      <div><label>每号每分钟上限</label><input type="number" id="rate" min="0"></div>
      <div><label>回复字数上限</label><input type="number" id="maxRunes" min="0"></div>
      <div><label>上下文轮数</label><input type="number" id="histPairs" min="0"></div>
      <div><label>白名单群（逗号分隔）</label><input type="text" id="groups" placeholder="992511553"></div>
      <div><label>免 @ 群（逗号分隔）</label><input type="text" id="noAt" placeholder="992511553"></div>
      <div><label>私聊白名单（逗号分隔）</label><input type="text" id="users" placeholder="941628696"></div>
      <div><label>忽略的 QQ（逗号分隔）</label><input type="text" id="ignored" placeholder="190839970,2854199922"></div>
    </div>
    <div class="row">
      <label style="margin:0"><input type="checkbox" id="enabled"> 启用机器人</label>
      <label style="margin:0"><input type="checkbox" id="requireAt"> 群内默认需 @</label>
      <button id="save">保存并热生效</button>
      <span class="muted" id="saveHint"></span>
    </div>
  </div>

  <div class="card" style="margin-top:12px">
    <h2>用户记忆（每人独立上下文）</h2>
    <table><thead><tr><th>QQ</th><th>昵称</th><th>次数</th><th>最后活跃</th><th>最近说过</th><th></th></tr></thead><tbody id="memBody"></tbody></table>
  </div>

  <div class="card" style="margin-top:12px">
    <h2>最近事件</h2>
    <table><thead><tr><th>时间</th><th>类型</th><th>群</th><th>用户</th><th>@</th><th>结果</th></tr></thead><tbody id="evBody"></tbody></table>
  </div>
</main>
<div id="toast"></div>
<script>
const $ = (id) => document.getElementById(id);
const qs = new URLSearchParams(location.search);
if (qs.get('token')) sessionStorage.setItem('qqbotAdminToken', qs.get('token'));
const token = sessionStorage.getItem('qqbotAdminToken') || '';

async function api(path, opts) {
  opts = opts || {};
  opts.headers = Object.assign({'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'}, opts.headers || {});
  const r = await fetch(path, opts);
  if (r.status === 401) { toast('token 无效', true); throw new Error('unauthorized'); }
  const d = await r.json();
  if (!d.ok) throw new Error(d.error || 'failed');
  return d;
}
function toast(msg, bad) {
  const t = $('toast'); t.textContent = msg;
  t.style.borderColor = bad ? 'var(--err)' : 'var(--ok)';
  t.classList.add('show'); setTimeout(() => t.classList.remove('show'), 2200);
}
const fmtUptime = (s) => s > 86400 ? Math.floor(s/86400)+'天' : s > 3600 ? Math.floor(s/3600)+'小时' : Math.floor(s/60)+'分';

async function load() {
  const d = await api('/admin/api/state');
  const c = d.config, st = d.stats;
  $('pillEnabled').textContent = c.enabled ? '已启用' : '已停用';
  $('pillEnabled').className = 'pill ' + (c.enabled ? 'ok' : 'err');
  $('pillModel').textContent = c.model;
  $('pillUptime').textContent = '运行 ' + fmtUptime(st.uptime_sec);
  $('kEvents').textContent = st.events;
  $('kReplied').textContent = st.replied;
  $('kSkipped').textContent = st.skipped;
  $('kMem').textContent = st.memory_users;

  $('persona').value = c.system_prompt || '';
  $('model').value = c.model || '';
  $('rate').value = c.rate_per_minute || 0;
  $('maxRunes').value = c.max_reply_runes || 0;
  $('histPairs').value = c.max_history_pairs || 0;
  $('groups').value = (c.allowed_groups || []).join(',');
  $('noAt').value = (c.no_at_groups || []).join(',');
  $('users').value = (c.allowed_users || []).join(',');
  $('ignored').value = (c.ignored_users || []).join(',');
  $('enabled').checked = !!c.enabled;
  $('requireAt').checked = !!c.require_at_in_group;

  $('memBody').innerHTML = (d.memory || []).length ? d.memory.map(m =>
    '<tr><td><code>' + m.uid + '</code></td><td>' + esc(m.nick || '—') + '</td><td>' + m.count +
    '</td><td class="muted">' + m.last_seen + '</td><td class="muted">' + esc((m.notes || []).join(' / ')) +
    '</td><td><button class="ghost" data-forget="' + m.uid + '">忘掉</button></td></tr>').join('')
    : '<tr><td colspan="6" class="muted">还没有记忆：等群里有人说几句话</td></tr>';

  $('evBody').innerHTML = (d.recent || []).slice().reverse().map(e =>
    '<tr><td class="muted">' + e.time.slice(11,19) + '</td><td>' + e.type + '</td><td>' + (e.group||'—') +
    '</td><td>' + (e.user||'—') + (e.nick ? ' <span class="muted">' + esc(e.nick) + '</span>' : '') +
    '</td><td>' + (e.at ? '是' : '否') + '</td><td class="' + (e.action === 'replied' ? 'ok' : 'muted') + '">' + e.action + '</td></tr>').join('')
    || '<tr><td colspan="6" class="muted">暂无事件</td></tr>';
}
function esc(s) { return String(s).replace(/[&<>"]/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
const nums = (v) => v.split(',').map(x => parseInt(x.trim(), 10)).filter(x => !isNaN(x));

$('save').onclick = async () => {
  $('save').disabled = true;
  try {
    await api('/admin/api/config', {method: 'POST', body: JSON.stringify({
      system_prompt: $('persona').value,
      model: $('model').value,
      rate_per_minute: Number($('rate').value || 0),
      max_reply_runes: Number($('maxRunes').value || 0),
      max_history_pairs: Number($('histPairs').value || 0),
      allowed_groups: nums($('groups').value),
      no_at_groups: nums($('noAt').value),
      allowed_users: nums($('users').value),
      ignored_users: nums($('ignored').value),
      enabled: $('enabled').checked,
      require_at_in_group: $('requireAt').checked,
    })});
    toast('已保存并热生效');
    await load();
  } catch (e) { toast('保存失败：' + e.message, true); }
  $('save').disabled = false;
};
document.addEventListener('click', async (ev) => {
  const uid = ev.target.getAttribute && ev.target.getAttribute('data-forget');
  if (!uid) return;
  if (!confirm('清除 QQ ' + uid + ' 的记忆？')) return;
  try { await api('/admin/api/memory/forget', {method:'POST', body: JSON.stringify({uid: Number(uid)})}); toast('已清除'); load(); }
  catch (e) { toast('失败：' + e.message, true); }
});
load().catch(e => toast('加载失败：' + e.message, true));
setInterval(() => load().catch(()=>{}), 5000);
</script>
</body>
</html>`
