const out = (o) => process.stdout.write('QQBOT_RESULT ' + JSON.stringify(o) + '\n');

const task = await taskSpace('verify qqbot view');
const page = task.page('p1');

await page.goto('http://ct-game.com:7863/panel/');
await page.goto('http://ct-game.com:7863/panel/#qqbot');
await new Promise((r) => setTimeout(r, 5000));

const res = await page.evaluate(() => ({
  tokForms: document.querySelectorAll('#view-qqbot .tok-form').length,
  hasPersona: !!document.querySelector('#qbPersona'),
  hasSave: !!document.querySelector('#qbSave'),
  tables: document.querySelectorAll('#view-qqbot table.tok-tbl').length,
  flds: document.querySelectorAll('.tok-form .fld').length,
  viewVisible: (() => { const s = document.querySelector('#view-qqbot'); return !!s && !s.hasAttribute('hidden'); })(),
  personaLen: (() => { const t = document.querySelector('#qbPersona'); return t ? t.value.length : -1; })(),
  url: location.href
}));

out(res);
await task.finish({ keep: [] });
