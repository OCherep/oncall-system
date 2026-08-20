/* Patches task status UX for role=user after main index.html loads */
(function patch(){
  function apply(){
    if (typeof window.nextStatuses !== 'function') { setTimeout(apply, 50); return; }

    window.nextStatuses = function(cur){
      var admin = !!(window.U && window.U.role === 'admin');
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

    window.updTask = async function(id, field, val){
      var body = {
        id: id,
        role: (window.U && window.U.role) || 'user',
        actor: (window.U && window.U.name) || ''
      };
      body[field] = val;
      var r = await fetch('/api/daily-tasks', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });
      if (!r.ok) alert(await r.text() || 'Помилка');
      if (typeof window.load === 'function') await window.load();
      var col = document.getElementById('m-col');
      if (col && col.style.display === 'flex' && typeof window.openCol === 'function') {
        window.openCol(window._colKind, window._colName);
      } else if (window._day && typeof window.day === 'function') {
        window.day(window._day);
      }
    };

    window.updInc = async function(id, status){
      var r = await fetch('/api/incidents', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: id,
          status: status,
          role: (window.U && window.U.role) || 'user',
          actor: (window.U && window.U.name) || ''
        })
      });
      if (!r.ok) alert(await r.text() || 'Помилка');
      if (typeof window.load === 'function') await window.load();
      if (typeof window.openCol === 'function') window.openCol(window._colKind, window._colName);
    };

    console.log('[oncall] status-fix applied');
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', apply);
  else apply();
})();
