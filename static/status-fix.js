/* Override fragile status helpers — load after index inline script if needed */
(function(){
  window.nextStatuses = function(cur){
    var admin = window.U && window.U.role === 'admin';
    var map = {
      'Нова': admin ? ['Нова','У роботі','Архів'] : ['Нова','У роботі'],
      'У роботі': admin ? ['У роботі','На паузі','До перевірки','Виконана','Архів'] : ['У роботі','На паузі','До перевірки'],
      'На паузі': admin ? ['На паузі','У роботі','До перевірки','Архів'] : ['На паузі','У роботі'],
      'До перевірки': admin ? ['До перевірки','У роботі','Виконана','Архів'] : ['До перевірки','Виконана'],
      'Виконана': admin ? ['Виконана','Перевідкрита','Архів'] : ['Виконана'],
      'Перевідкрита': admin ? ['Перевідкрита','Нова','У роботі','Архів'] : ['Перевідкрита'],
      'Архів': admin ? ['Архів','Перевідкрита','Нова'] : ['Архів']
    };
    return map[cur] || (admin ? ['Нова','У роботі','На паузі','До перевірки','Виконана','Перевідкрита','Архів'] : [cur || 'Нова']);
  };
  var _upd = window.updTask;
  window.updTask = async function(id, field, val){
    var body = { id: id, role: (window.U && window.U.role) || 'user', actor: (window.U && window.U.name) || '' };
    body[field] = val;
    var r = await fetch('/api/daily-tasks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) alert(await r.text() || 'Помилка');
    await window.load();
    if (document.getElementById('m-col') && document.getElementById('m-col').style.display === 'flex') window.openCol(window._colKind, window._colName);
    else if (window._day) window.day(window._day);
  };
  var _updInc = window.updInc;
  window.updInc = async function(id, status){
    var r = await fetch('/api/incidents', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: id, status: status, role: (window.U && window.U.role) || 'user', actor: (window.U && window.U.name) || '' }) });
    if (!r.ok) alert(await r.text() || 'Помилка');
    await window.load();
    window.openCol(window._colKind, window._colName);
  };
})();
