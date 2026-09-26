// delivery-chat.js — the AI chat island of the public-site HTML face.
// The shell is server-rendered; messages stream from the member-auth JSON
// face POST /api/public/sites/{slug}/chat (SSE over fetch). No framework,
// no external requests beyond the same-origin API.
(function () {
  "use strict";
  var shell = document.getElementById("ask-shell");
  if (!shell) return;
  var slug = shell.getAttribute("data-site-slug") || "";
  var loggedIn = shell.getAttribute("data-logged-in") === "true";
  var loginHref = shell.getAttribute("data-login-href") || "/login";
  var quotaUsed = parseInt(shell.getAttribute("data-quota-used") || "0", 10);
  var quotaLimit = parseInt(shell.getAttribute("data-quota-limit") || "20", 10);
  var messages = document.getElementById("chat-messages");
  var form = document.getElementById("chat-form");
  var input = document.getElementById("chat-input");
  var send = document.getElementById("chat-send");
  var quotaEl = document.querySelector(".ask-quota");
  var sessionId = shell.getAttribute("data-session-id") || "";
  var busy = false;

  function quotaLeft() {
    return Math.max(0, quotaLimit - quotaUsed);
  }
  function renderQuota() {
    if (!loggedIn) return; // 匿名访客：无额度徽章，不禁输入（登录闸在提交时）
    if (quotaEl) quotaEl.textContent = "今日额度 " + quotaUsed + "/" + quotaLimit;
    if (quotaLeft() <= 0) {
      input.disabled = true;
      send.disabled = true;
      input.placeholder = "今日额度已用完，明日恢复";
    }
  }
  function appendBubble(role, text) {
    var bubble = document.createElement("div");
    bubble.className = "chat-msg chat-msg-" + role;
    bubble.textContent = text;
    messages.appendChild(bubble);
    messages.scrollTop = messages.scrollHeight;
    return bubble;
  }
  function appendSources(refs) {
    if (!refs || !refs.length) return;
    var box = document.createElement("div");
    box.className = "chat-sources";
    refs.forEach(function (ref) {
      var link = document.createElement("a");
      link.href = ref.url;
      link.textContent = ref.title;
      box.appendChild(link);
    });
    messages.appendChild(box);
    messages.scrollTop = messages.scrollHeight;
  }

  form.addEventListener("submit", function (event) {
    event.preventDefault();
    event.stopPropagation();
    var question = (input.value || "").trim();
    if (!question || busy) return;
    // 登录闸门在提问动作上：匿名访客可看可输入，点发送时才引导登录。
    if (!loggedIn) {
      var tip = appendBubble("ai", "AI 问答需要登录后使用（登录后每日可提问 " + quotaLimit + " 次）。正在前往登录页…");
      tip.className += " chat-msg-login";
      busy = true;
      setTimeout(function () { window.location.href = loginHref; }, 1200);
      return;
    }
    if (quotaLeft() <= 0) {
      appendBubble("ai", "今日额度已用完，明日恢复。");
      return;
    }
    busy = true;
    send.disabled = true;
    input.value = "";
    appendBubble("user", question);
    var bubble = appendBubble("ai", "…");
    var started = Date.now();
    fetch("/api/public/sites/" + slug + "/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ session_id: sessionId, message: question }),
    })
      .then(function (response) {
        if (response.status === 401) {
          window.location.href = loginHref;
          return null;
        }
        if (response.status === 429) {
          quotaUsed = quotaLimit;
          renderQuota();
          bubble.textContent = "今日额度已用完，明日恢复。";
          busy = false;
          send.disabled = false;
          return null;
        }
        if (!response.ok || !response.body) {
          bubble.textContent = "服务暂时不可用，请稍后重试。";
          busy = false;
          send.disabled = false;
          return null;
        }
        return response.body;
      })
      .then(function (body) {
        if (!body) return;
        var reader = body.getReader();
        var decoder = new TextDecoder();
        var buffer = "";
        function pump() {
          return reader.read().then(function (chunk) {
            if (chunk.done) {
              busy = false;
              send.disabled = false;
              renderQuota();
              return;
            }
            buffer += decoder.decode(chunk, { stream: true });
            var frames = buffer.split("\n\n");
            buffer = frames.pop();
            frames.forEach(function (frame) {
              var line = frame.split("\n").find(function (l) {
                return l.indexOf("data:") === 0;
              });
              if (!line) return;
              var payload;
              try {
                payload = JSON.parse(line.slice(5).trim());
              } catch (e) {
                return;
              }
              if (payload.type === "delta" && payload.text) {
                if (bubble.textContent === "…") bubble.textContent = "";
                bubble.textContent += payload.text;
                messages.scrollTop = messages.scrollHeight;
              } else if (payload.type === "done") {
                if (payload.session_id) sessionId = payload.session_id;
                if (typeof payload.quota_used === "number") quotaUsed = payload.quota_used;
                appendSources(payload.references);
                busy = false;
                send.disabled = false;
                renderQuota();
              } else if (payload.type === "error") {
                bubble.textContent = payload.message || "服务暂时不可用。";
                busy = false;
                send.disabled = false;
              }
            });
            return pump();
          });
        }
        var elapsed = Date.now() - started;
        return pump();
      })
      .catch(function () {
        bubble.textContent = "网络异常，请稍后重试。";
        busy = false;
        send.disabled = false;
      });
  });
  renderQuota();
})();
