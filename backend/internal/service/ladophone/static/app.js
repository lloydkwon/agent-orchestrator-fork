// LADO phone approval: logging in with this phone's passkey, the approvals
// waiting, one approval in detail and approving it with its own passkey
// signature. Everything the server sends goes in with textContent only.
//
// Approving takes two taps: «승인» asks the server to read the approval
// again and prepare a signature over exactly what is shown (which can take
// a few seconds for an integration), then «Face ID 로 서명해 승인» opens the
// passkey sheet right at the tap, as Safari wants.
"use strict";
(() => {
  const W = window.LadoWebAuthn;
  const $ = (id) => document.getElementById(id);
  const msg = $("msg");
  const screens = ["login", "list", "detail", "done"];

  // Words for the server's refusals, by code. A code not here shows the
  // server's own message.
  const REFUSALS = {
    LADO_PHONE_STALE: "내용이 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_PHONE_NOT_APPROVABLE: "지금은 승인할 수 없습니다. 다시 읽어 확인하세요.",
    LADO_PHONE_DESKTOP_ONLY: "운영 DB SQL 이 있어 데스크톱 앱에서만 승인할 수 있습니다.",
    LADO_PHONE_CEREMONY_STALE: "시간이 지났거나 이미 쓴 서명입니다. 승인을 다시 누르세요.",
    LADO_PHONE_SIGNATURE_FAILED: "서명을 확인하지 못했습니다. 승인을 다시 누르세요.",
    LADO_PHONE_LOCKED: "패스키 확인이 여러 번 실패해 잠시 쉽니다. 5분 뒤 다시 하세요.",
    LADO_PHONE_OFF: "폰 승인이 꺼져 있습니다. 데스크톱 앱에서 켜세요.",
    LADO_PHONE_SERVE_MOVED: "Tailscale serve 설정이 바뀌어 승인하지 않았습니다. 데스크톱 앱을 확인하세요.",
    LADO_PHONE_PASSKEY_REVOKED: "이 폰의 패스키가 철회되었습니다.",
    LADO_PHONE_APPROVALS_UNAVAILABLE: "LADO 승인 서비스가 연결되어 있지 않습니다.",
    LADO_PLAN_SUPERSEDED: "더 새 계획이 있습니다 — 다시 읽기를 누르세요.",
    LADO_PLAN_CHANGED: "계획이 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_PLAN_INVALID: "이 계획에는 문제가 있어 승인할 수 없습니다.",
    LADO_RULES_CHANGED: "규칙이 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_TASK_RUNNING: "이 작업의 워커가 아직 돌고 있어 승인할 수 없습니다.",
    LADO_TASK_ALREADY_QUEUED: "이미 승인되어 워커를 기다리는 계획입니다.",
    LADO_INTEGRATION_HEAD_CHANGED: "PR 이 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_INTEGRATION_VERIFICATION_CHANGED: "검증 결과가 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_INTEGRATION_PRS_CHANGED: "PR 목록이 바뀌었습니다 — 다시 읽기를 누르세요.",
    LADO_INTEGRATION_NOT_READY: "LADO 가 아직 검증 중이거나 검사가 실패했습니다.",
    LADO_INTEGRATION_ALREADY_APPROVED: "이미 승인된 통합입니다.",
  };
  // Refusals that mean what was shown is out of date.
  const REREAD = new Set([
    "LADO_PHONE_STALE", "LADO_PHONE_NOT_APPROVABLE", "LADO_PLAN_SUPERSEDED", "LADO_PLAN_CHANGED", "LADO_RULES_CHANGED",
    "LADO_INTEGRATION_HEAD_CHANGED", "LADO_INTEGRATION_VERIFICATION_CHANGED", "LADO_INTEGRATION_PRS_CHANGED",
  ]);

  const LOGIN_RESULTS = {
    none: "등록된 패스키가 없습니다. 데스크톱 앱에서 이 폰을 먼저 등록하세요.",
    closed: "폰 승인이 꺼져 있습니다. 데스크톱 앱에서 켜세요.",
    locked: "패스키 확인이 여러 번 실패해 잠시 쉽니다. 5분 뒤 다시 하세요.",
    stale: "시간이 지났습니다. 다시 누르세요.",
    failed: "패스키를 확인하지 못했습니다. 다시 누르세요.",
    broken: "LADO 가 로그인을 준비하지 못했습니다. 데스크톱 앱을 확인하세요.",
  };

  function say(text, tone) {
    msg.textContent = text || "";
    msg.dataset.tone = tone || "";
  }

  function show(name) {
    for (const s of screens) $(s).hidden = s !== name;
  }

  function refusal(res) {
    const body = res.body || {};
    if (res.status === 403 && !body.code) return { code: "", text: W.FORBIDDEN };
    const text = REFUSALS[body.code] || `LADO 가 거절했습니다: ${body.message || res.status}${body.code ? ` (${body.code})` : ""}`;
    return { code: body.code || "", text };
  }

  // A passkey sheet open at a time; a new one closes the old.
  let sheet = null;
  function openSheet(publicKey) {
    if (sheet) sheet.abort();
    sheet = new AbortController();
    return navigator.credentials.get({ publicKey, signal: sheet.signal });
  }

  // ---- Login. The challenge is fetched ahead, so the tap opens the sheet
  // with nothing awaited before it. The server keeps it three minutes.
  const FRESH_MS = 2 * 60 * 1000;
  const REFRESH_MS = 90 * 1000;
  const loginButton = $("login-button");
  let login = null; // {ceremony, publicKey, at}
  let loggingIn = false;

  async function prepareLogin() {
    if (loggingIn || $("login").hidden) return;
    try {
      const res = await W.send("POST", "/login/begin", {});
      if (res.status !== 200 || !res.body) {
        login = null;
        loginButton.disabled = true;
        say(refusal(res).text, "stop");
        return;
      }
      if (res.body.result !== "ok") {
        login = null;
        loginButton.disabled = true;
        say(LOGIN_RESULTS[res.body.result] || `실패: ${res.body.result}`, "stop");
        return;
      }
      login = { ceremony: res.body.ceremony, publicKey: W.requestOptions(res.body.options), at: Date.now() };
      loginButton.disabled = false;
    } catch (e) {
      login = null;
      loginButton.disabled = true;
      say(e.message, "stop");
    }
  }

  function pressLogin() {
    if (loggingIn) return;
    const l = login;
    if (!l || Date.now() - l.at > FRESH_MS) {
      say("준비하는 중입니다. 잠시 뒤 다시 누르세요.", "");
      prepareLogin();
      return;
    }
    login = null;
    finishLogin(l, openSheet(l.publicKey));
  }

  async function finishLogin(l, pending) {
    loggingIn = true;
    loginButton.disabled = true;
    say("패스키를 확인하는 중입니다…", "");
    try {
      let cred;
      try {
        cred = await pending;
      } catch (e) {
        say(W.credentialError(e), "stop");
        return;
      }
      const res = await W.send("POST", "/login/finish", { ceremony: l.ceremony, credential: W.assertionJSON(cred) });
      if (res.status === 200 && res.body && res.body.result === "ok") {
        say("", "");
        await loadList();
        return;
      }
      say(res.body ? LOGIN_RESULTS[res.body.result] || `실패: ${res.body.result}` : refusal(res).text, "stop");
    } catch (e) {
      say(e.message, "stop");
    } finally {
      loggingIn = false;
      prepareLogin();
    }
  }

  function needLogin() {
    show("login");
    prepareLogin();
  }

  // ---- The approvals waiting.
  async function loadList() {
    let res;
    try {
      res = await W.send("GET", "/api/approvals");
    } catch (e) {
      say(e.message, "stop");
      return;
    }
    if (res.status === 401) {
      needLogin();
      return;
    }
    if (res.status !== 200 || !res.body) {
      say(refusal(res).text, "stop");
      return;
    }
    const items = $("items");
    items.replaceChildren();
    for (const item of res.body.items || []) items.append(itemRow(item));
    $("empty").hidden = items.childElementCount > 0;
    show("list");
  }

  function span(cls, text) {
    const el = document.createElement("span");
    el.className = cls;
    el.textContent = text;
    return el;
  }

  function itemRow(item) {
    const li = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.append(
      span("kind", item.kind === "plan" ? "계획 승인" : "통합 승인"),
      span("item-title", item.title || "(제목 없음)"),
      span("item-line", item.line || ""),
      span("item-where", `${item.project} · ${item.issue}`),
    );
    button.addEventListener("click", () => openDetail(item.kind, item.id));
    li.append(button);
    return li;
  }

  // ---- One approval.
  let current = null; // {kind, id, binding}
  let signing = null; // {ceremony, publicKey}
  let working = false;

  async function openDetail(kind, id) {
    current = null;
    signing = null;
    say(kind === "integration" ? "LADO 가 PR 을 다시 확인하는 중입니다…" : "읽는 중입니다…", "");
    let res;
    try {
      res = await W.send("GET", `/api/approvals/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`);
    } catch (e) {
      say(e.message, "stop");
      return;
    }
    if (res.status === 401) {
      needLogin();
      return;
    }
    if (res.status !== 200 || !res.body) {
      say(refusal(res).text, "stop");
      return;
    }
    say("", "");
    renderDetail(res.body);
    show("detail");
  }

  function renderDetail(d) {
    current = { kind: d.kind, id: d.id, binding: d.binding };
    $("detail-title").textContent = d.title;
    const facts = $("facts");
    facts.replaceChildren();
    for (const line of d.lines || []) {
      if (line === "") {
        const gap = document.createElement("div");
        gap.className = "gap";
        facts.append(gap);
        continue;
      }
      const p = document.createElement("p");
      p.textContent = line;
      facts.append(p);
    }
    $("next").textContent = d.next || "";
    $("next").hidden = !d.next;
    $("agent-text").textContent = d.agentText || "";
    $("agent").hidden = !d.agentText;
    $("agent").open = false;
    $("reason").textContent = d.desktopOnly ? "" : d.reason || "";
    $("reason").hidden = d.desktopOnly || !d.reason;
    $("desktop-only").hidden = !d.desktopOnly;
    $("approve").hidden = !d.approvable;
    $("approve").disabled = false;
    $("sign").hidden = true;
    $("reread").hidden = d.approvable;
  }

  function refused(res) {
    const r = refusal(res);
    if (res.status === 401) {
      needLogin();
      return;
    }
    say(r.text, "stop");
    if (REREAD.has(r.code)) {
      $("approve").hidden = true;
      $("sign").hidden = true;
      $("reread").hidden = false;
    }
  }

  async function pressApprove() {
    if (working || !current) return;
    working = true;
    $("approve").disabled = true;
    say(current.kind === "integration" ? "LADO 가 PR 을 다시 확인하고 서명을 준비하는 중입니다…" : "서명을 준비하는 중입니다…", "");
    try {
      const res = await W.send("POST", "/api/approve/begin", { kind: current.kind, binding: current.binding });
      if (res.status !== 200 || !res.body || res.body.result !== "ok") {
        $("approve").disabled = false;
        refused(res);
        return;
      }
      signing = { ceremony: res.body.ceremony, publicKey: W.requestOptions(res.body.options) };
      $("approve").hidden = true;
      $("sign").hidden = false;
      say("위 내용 그대로 승인합니다. 2분 안에 Face ID 로 서명하세요.", "");
    } catch (e) {
      $("approve").disabled = false;
      say(e.message, "stop");
    } finally {
      working = false;
    }
  }

  function pressSign() {
    if (working || !signing) return;
    finishApproval(signing, openSheet(signing.publicKey));
  }

  async function finishApproval(s, pending) {
    working = true;
    $("sign").disabled = true;
    try {
      let cred;
      try {
        cred = await pending;
      } catch (e) {
        // The ceremony still waits on the server; the sign button stays.
        say(W.credentialError(e), "stop");
        return;
      }
      signing = null;
      say("LADO 에 승인을 보내는 중입니다…", "");
      const res = await W.send("POST", "/api/approve/finish", { ceremony: s.ceremony, credential: W.assertionJSON(cred) });
      if (res.status === 200 && res.body && res.body.result === "ok") {
        say("", "");
        $("done-text").textContent = `${$("detail-title").textContent} — 폰의 패스키로 승인했습니다.`;
        show("done");
        return;
      }
      // A ceremony is used once; a new approval starts over.
      $("sign").hidden = true;
      $("approve").hidden = false;
      $("approve").disabled = false;
      refused(res);
    } catch (e) {
      say(e.message, "stop");
    } finally {
      working = false;
      $("sign").disabled = false;
    }
  }

  if (!W.supported()) {
    say("이 브라우저는 패스키를 지원하지 않습니다.", "stop");
    return;
  }
  loginButton.addEventListener("click", pressLogin);
  $("refresh").addEventListener("click", () => loadList());
  $("back").addEventListener("click", () => loadList());
  $("done-back").addEventListener("click", () => loadList());
  $("approve").addEventListener("click", pressApprove);
  $("sign").addEventListener("click", pressSign);
  $("reread").addEventListener("click", () => {
    if (current) openDetail(current.kind, current.id);
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState !== "visible") return;
    if (!$("login").hidden) prepareLogin();
    else if (!$("list").hidden) loadList();
  });
  setInterval(prepareLogin, REFRESH_MS);
  loadList();
})();
