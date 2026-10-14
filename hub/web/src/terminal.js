// The terminal of an agent page: xterm.js bridged to the hub's websocket. Binary frames are the terminal's bytes, text frames are
// small JSON control messages (the size going up; "busy", "taken" and errors coming down).
(function () {
  "use strict";
  var open = document.getElementById("terminal-open");
  if (!open) return;
  var close = document.getElementById("terminal-close");
  var takeover = document.getElementById("terminal-takeover");
  var mode = document.getElementById("terminal-mode");
  var host = document.getElementById("terminal");
  var socket = null;
  var term = null;
  var ended = "";

  function show(text, warn) {
    mode.hidden = false;
    mode.textContent = text;
    mode.className = "badge" + (warn ? " warn" : "");
  }

  function connect(takingOver) {
    host.hidden = false;
    open.hidden = true;
    takeover.hidden = true;
    close.hidden = false;
    if (!term) {
      term = new window.Terminal({ cursorBlink: true, fontFamily: "ui-monospace, Menlo, Consolas, monospace", fontSize: 14, convertEol: false });
      term.fit = new window.FitAddon.FitAddon();
      term.loadAddon(term.fit);
      term.open(host);
    } else {
      term.reset();
    }
    term.fit.fit();
    var scheme = window.location.protocol === "https:" ? "wss://" : "ws://";
    socket = new WebSocket(scheme + window.location.host + open.dataset.url + (takingOver ? "?takeover=1" : ""));
    socket.binaryType = "arraybuffer";
    var ws = socket;
    ws.onopen = function () {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      show("connected");
    };
    ws.onmessage = function (event) {
      if (typeof event.data === "string") {
        var message = JSON.parse(event.data);
        if (message.type === "busy") {
          ended = "in use by " + message.holder;
          takeover.hidden = false;
        }
        if (message.type === "taken") ended = "taken over by " + message.holder;
        if (message.type === "error") show("error: " + message.message, true);
        return;
      }
      term.write(new Uint8Array(event.data));
    };
    ws.onclose = function () {
      if (ws !== socket) return;
      show(ended || "disconnected", true);
      ended = "";
      open.hidden = false;
      open.textContent = "Reconnect";
      close.hidden = true;
      socket = null;
    };
  }

  var encoder = new TextEncoder();
  open.addEventListener("click", function () {
    connect();
    if (!term.__wired) {
      term.__wired = true;
      term.onData(function (data) {
        if (socket && socket.readyState === WebSocket.OPEN) socket.send(encoder.encode(data));
      });
      term.onResize(function (size) {
        if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "resize", cols: size.cols, rows: size.rows }));
      });
      window.addEventListener("resize", function () { if (term) term.fit.fit(); });
    }
    term.focus();
  });
  takeover.addEventListener("click", function () {
    connect(true);
    term.focus();
  });
  close.addEventListener("click", function () { if (socket) socket.close(); });
})();
