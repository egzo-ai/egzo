// Confirmations for destructive buttons, and the refresh of the live panel of a page (status, agents, timeline).
(function () {
  "use strict";

  document.addEventListener("submit", function (event) {
    var message = event.target.getAttribute("data-confirm");
    if (message && !window.confirm(message)) event.preventDefault();
  });

  var live = document.getElementById("live");
  if (!live || !live.dataset.refresh) return;
  var busy = false;
  function refresh() {
    if (busy || document.hidden) return;
    busy = true;
    fetch(live.dataset.refresh, { credentials: "same-origin", headers: { "X-Requested-With": "fetch" } })
      .then(function (response) {
        if (response.status === 401) { window.location.reload(); return null; }
        return response.ok ? response.text() : null;
      })
      .then(function (html) {
        if (html !== null && html !== live.innerHTML) live.innerHTML = html;
      })
      .catch(function () {})
      .finally(function () { busy = false; });
  }
  setInterval(refresh, 3000);
})();
