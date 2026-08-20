/* Patches task status UX for role=user — include current status in select + send actor */
(function(){
  function getU(){
    try { return JSON.parse(localStorage.getItem('u')); } catch(e) { return null; }
  }

  function apply(){
    if (typeof window.nextStatuses !== 'function' || typeof window.updTask !== 'function') {
      setTimeout(apply, 80);
      return;
    }

    window.nextStatuses = function(cur){
      var u = getU();
      var admin = !!(u && u.role === 'admin');
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
      var u = getU();
      var body = {
        id: id,
        role: (u && u.role) || 'user',
        actor: (u && u.name) || ''
      };
      body[field] = val;
      var r = await fetch('/api/daily-tasks', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });
      if (!r.ok) { alert(await r.text() || 'Помилка'); return; }
      if (typeof window.load === 'function') await window.load();
      refreshOpenModals();
    };

    window.updInc = async function(id, status){
      var u = getU();
      var r = await fetch('/api/incidents', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: id,
          status: status,
          role: (u && u.role) || 'user',
          actor: (u && u.name) || ''
        })
      });
      if (!r.ok) { alert(await r.text() || 'Помилка'); return; }
      if (typeof window.load === 'function') await window.load();
      refreshOpenModals();
    };

    function refreshOpenModals(){
      var col = document.getElementById('m-col');
      if (col && col.style.display === 'flex' && typeof window.openCol === 'function') {
        var title = (document.getElementById('col-title') || {}).innerText || '';
        // "Задачі з дейлі · devops5 · 2026-08-20" or "Звернення · ..."
        var parts = title.split(' · ');
        if (parts.length >= 2) {
          var kind = title.indexOf('Звернення') === 0 ? 'inc' : 'task';
          var name = parts[1].trim();
          window.openCol(kind, name);
          return;
        }
      }
      var dayM = document.getElementById('m-day');
      if (dayM && dayM.style.display === 'flex' && typeof window.day === 'function') {
        var dt = (document.getElementById('dt') || {}).innerText || '';
        var m = dt.match(/(\d{4}-\d{2}-\d{2})/);
        if (m) window.day(m[1]);
      }
    }

    console.log('[oncall] status-fix applied');
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', apply);
  else apply();
})();
