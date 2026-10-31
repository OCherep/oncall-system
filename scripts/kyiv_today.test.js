// node scripts/kyiv_today.test.js — перевіряє kyivToday() з static/index.html і admin.html.
const fs = require('fs'), path = require('path'), assert = require('assert');
for (const f of ['index.html', 'admin.html']) {
  const src = fs.readFileSync(path.join(__dirname, '..', 'static', f), 'utf8');
  const m = src.match(/function kyivToday\(d\)\{[\s\S]*?\n/);
  assert(m, 'kyivToday not found in ' + f);
  const kyivToday = new Function(m[0] + '\nreturn kyivToday;')();
  const cases = [
    ['2026-10-31T22:30:00Z', '2026-11-01'], // 00:30 EET
    ['2026-07-01T21:30:00Z', '2026-07-02'], // 00:30 EEST
    ['2026-10-24T21:30:00Z', '2026-10-25'], // ніч переходу на зимовий час
    ['2026-10-25T21:59:00Z', '2026-10-25'], // 23:59 EET
    ['2026-10-25T22:00:00Z', '2026-10-26'],
  ];
  for (const [iso, want] of cases) assert.strictEqual(kyivToday(new Date(iso)), want, f + ' ' + iso);
  console.log('ok', f, cases.length, 'cases');
}
