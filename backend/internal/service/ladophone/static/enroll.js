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

  const W = window.LadoWebAuthn;

  async function call(path, body) {
    const { status, body: data } = await W.send("POST", path, body);
    if (status === 403) throw new Error(W.FORBIDDEN);
    if (status !== 200 || !data) throw new Error(`LADO 오류 ${status}`);
    return data;
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
      ready = { ceremony: begun.ceremony, publicKey: W.creationOptions(begun.options), at: Date.now() };
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
        say(W.credentialError(e), "stop");
        return;
      }
      const done = await call("/enroll/finish", { ceremony: r.ceremony, credential: W.attestationJSON(cred) });
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

  if (!W.supported()) {
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
