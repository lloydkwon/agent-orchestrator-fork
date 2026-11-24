// LADO phone approval: making this phone's passkey. The server answers with a
// result word; the words for people are here. Text goes in with textContent
// only. The code the server returns is shown on this screen and nowhere else.
"use strict";
(() => {
  const $ = (id) => document.getElementById(id);
  const msg = $("msg");
  const start = $("start");
  const result = $("result");

  const RESULTS = {
    closed: "열린 등록이 없습니다. 데스크톱 앱에서 «폰 등록 시작»을 누른 뒤 다시 하세요.",
    stale: "시간이 지났습니다. 다시 누르세요.",
    failed: "패스키를 확인하지 못했습니다. 다시 누르세요.",
    conflict: "다른 등록이 함께 들어와 둘 다 버렸습니다. 데스크톱 앱에서 등록을 다시 시작하세요.",
    broken: "LADO 가 등록을 준비하지 못했습니다. 데스크톱 앱을 확인하세요.",
  };

  function say(text, tone) {
    msg.textContent = text;
    msg.dataset.tone = tone || "";
  }

  // base64url <-> ArrayBuffer for WebAuthn's byte fields.
  function toBuf(s) {
    let b = s.replace(/-/g, "+").replace(/_/g, "/");
    while (b.length % 4) b += "=";
    const bin = atob(b);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out.buffer;
  }
  function fromBuf(buf) {
    const bytes = new Uint8Array(buf);
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function creationOptions(o) {
    const pk = o.publicKey;
    return {
      ...pk,
      challenge: toBuf(pk.challenge),
      user: { ...pk.user, id: toBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map((c) => ({ ...c, id: toBuf(c.id) })),
    };
  }
  function attestationJSON(cred) {
    const r = cred.response;
    return {
      id: cred.id, rawId: fromBuf(cred.rawId), type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      response: {
        attestationObject: fromBuf(r.attestationObject),
        clientDataJSON: fromBuf(r.clientDataJSON),
        transports: typeof r.getTransports === "function" ? r.getTransports() : [],
      },
      clientExtensionResults: cred.getClientExtensionResults(),
    };
  }

  async function call(path, body) {
    let res;
    try {
      res = await fetch(path, {
        method: "POST", cache: "no-store", credentials: "same-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
    } catch (_) {
      throw new Error("LADO 의 답을 받지 못했습니다. 폰의 Tailscale 연결을 확인하세요.");
    }
    if (res.status === 403) throw new Error("접근이 거절됐습니다. 폰 승인을 켠 Tailscale 계정으로 열었는지 확인하세요.");
    if (!res.ok) throw new Error(`LADO 오류 ${res.status}`);
    return res.json();
  }

  function credentialError(e) {
    switch (e && e.name) {
      case "NotAllowedError": return "취소했거나 시간이 지났습니다. 다시 누르세요.";
      case "InvalidStateError": return "이 폰의 패스키는 이미 등록되어 있습니다.";
      case "SecurityError": return "이 주소에서는 패스키를 쓸 수 없습니다. 데스크톱 앱이 보여 준 https 주소로 여세요.";
      default: return `패스키 창이 실패했습니다: ${(e && e.name) || "오류"}`;
    }
  }

  // Safari may open the passkey sheet only right at the tap, so the
  // challenge is fetched ahead and the tap calls navigator.credentials with
  // no await before it. The server keeps a ceremony three minutes.
  const FRESH_MS = 2 * 60 * 1000;
  const REFRESH_MS = 90 * 1000;
  let ready = null; // {ceremony, publicKey, at}
  let working = false;
  let prepareFailed = false; // the message on screen is prepare's own failure

  async function prepare() {
    if (working || !result.hidden) return;
    try {
      const begun = await call("/enroll/begin", {});
      if (begun.result !== "ok") {
        ready = null;
        start.disabled = true;
        say(RESULTS[begun.result] || `실패: ${begun.result}`, "stop");
        prepareFailed = true;
        return;
      }
      ready = { ceremony: begun.ceremony, publicKey: creationOptions(begun.options), at: Date.now() };
      start.disabled = false;
      if (prepareFailed) say("", "");
      prepareFailed = false;
    } catch (e) {
      ready = null;
      start.disabled = true;
      say(e.message, "stop");
      prepareFailed = true;
    }
  }

  function press() {
    if (working) return;
    const r = ready;
    if (!r || Date.now() - r.at > FRESH_MS) {
      say("준비하는 중입니다. 잠시 뒤 다시 누르세요.", "");
      prepare();
      return;
    }
    ready = null; // a challenge is used once, on the server too
    finish(r, navigator.credentials.create({ publicKey: r.publicKey }));
  }

  async function finish(r, pending) {
    working = true;
    prepareFailed = false;
    start.disabled = true;
    say("패스키를 만드는 중입니다…", "");
    try {
      let cred;
      try {
        cred = await pending;
      } catch (e) {
        say(credentialError(e), "stop");
        return;
      }
      const done = await call("/enroll/finish", { ceremony: r.ceremony, credential: attestationJSON(cred) });
      if (done.result === "ok") {
        $("code").textContent = done.code;
        $("fingerprint").textContent = done.fingerprint;
        result.hidden = false;
        start.hidden = true;
        say("패스키를 만들었습니다. 아직 등록되지 않았습니다 — 데스크톱 앱에서 코드를 넣고 승인해야 끝납니다.", "ok");
        return;
      }
      say(RESULTS[done.result] || `실패: ${done.result}`, "stop");
    } catch (e) {
      say(e.message, "stop");
    } finally {
      working = false;
      if (result.hidden) prepare();
    }
  }

  if (!window.PublicKeyCredential || !navigator.credentials) {
    say("이 브라우저는 패스키를 지원하지 않습니다.", "stop");
    return;
  }
  start.addEventListener("click", press);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") prepare();
  });
  setInterval(prepare, REFRESH_MS);
  prepare();
})();
