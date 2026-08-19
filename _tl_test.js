const fs = require('fs');
const src = fs.readFileSync('web/kb/js/manju.js', 'utf8');
const m = src.match(/renderLog\(text\) \{[\s\S]*?\n    \},/);
const esc = (x) => String(x).replace(/&/g, '&amp;').replace(/</g, '&lt;');
const fakeLog = { dataset: {}, innerHTML: '', scrollHeight: 1000, scrollTop: 980, clientHeight: 40 };
global.document = { getElementById: () => fakeLog };
const $ = (id) => (id === 'manju-log' ? fakeLog : null);
const body = m[0].replace('renderLog(text) {', 'function renderLog(text) {').replace(/\},\s*$/, '}');
const fn = new Function('$', 'esc', body + '; return function(t){ return renderLog.call({}, t); };')($, esc);
fn([
  '[10:00:01] ━━━ 阶段 plan ━━━',
  '[10:00:05]   ✅ 4 角色 / 3 场景 / 15 镜头',
  '[10:00:06] ━━━ 阶段 render ━━━',
  '[10:01:00] [1/15] 镜头 1: [书院] 缓慢推近',
  '[10:01:00]   渲染提交 ab12cd34...',
  '[10:02:31] 🤖 审片 镜头 1: 85.2 分 ✅',
  '[10:03:00] ⚠️ 镜头 3 产物已过期',
  '[10:05:00] 🚨 镜头 5 预算耗尽',
  '[10:06:00] ❌ 阶段 render 失败',
].join('\n'));
const h = fakeLog.innerHTML;
const ck = {
  stage_left: /<span class="mj-tl-side"><b>方案<\/b>/.test(h),
  stage_tag: /<span class="mj-tl-side"><b>渲染<\/b><i>render<\/i>/.test(h),
  stage_grid: /<div class="mj-tl-row stage"[^>]*><span class="mj-tl-side">/.test(h),
  time_right: /<span class="mj-tl-main">镜头 1[^\u2026]*<\/span><span class="mj-tl-time">10:01:00<\/span>/.test(h.replace(/\uFF1A/g, ':')),
  dot_cell: /<span class="mj-tl-dot"><\/span>/.test(h),
  sub_no_dot_visible: /class="mj-tl-row sub info"/.test(h),
};
let fail = 0;
for (const [k, v] of Object.entries(ck)) { console.log(v ? 'PASS' : 'FAIL', k); if (!v) fail++; }
process.exit(fail);
