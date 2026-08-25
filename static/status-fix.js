// status-fix.js — ensure user can change own task statuses; always include current status in options
(function(){
  if (typeof nextStatuses === 'function') {
    const _orig = nextStatuses;
    window.nextStatuses = function(cur) {
      const list = _orig(cur) || [];
      if (cur && list.indexOf(cur) < 0) list.unshift(cur);
      return list;
    };
  }
})();
