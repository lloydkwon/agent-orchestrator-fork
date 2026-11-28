// LADO phone approval: the WebAuthn and request helpers both phone pages
// share. Byte fields travel as base64url. Where the browser has WebAuthn's
// JSON helpers (PublicKeyCredential.parseRequestOptionsFromJSON and
// credential.toJSON, Safari 18.4+ and Chrome 129+) they are used; elsewhere
// these functions do the same by hand. Nothing here touches the page.
"use strict";
window.LadoWebAuthn = (() => {
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

  // Registration options as the server sends them ({publicKey: …}).
  function creationOptions(o) {
    const pk = o.publicKey;
    return {
      ...pk,
      challenge: toBuf(pk.challenge),
      user: { ...pk.user, id: toBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map((c) => ({ ...c, id: toBuf(c.id) })),
    };
  }

  // Assertion options as the server sends them ({publicKey: …}).
  function requestOptions(o) {
    const pk = o.publicKey;
    if (window.PublicKeyCredential && typeof PublicKeyCredential.parseRequestOptionsFromJSON === "function") {
      return PublicKeyCredential.parseRequestOptionsFromJSON(pk);
    }
    return {
      ...pk,
      challenge: toBuf(pk.challenge),
      allowCredentials: (pk.allowCredentials || []).map((c) => ({ ...c, id: toBuf(c.id) })),
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

  function assertionJSON(cred) {
    if (typeof cred.toJSON === "function") return cred.toJSON();
    const r = cred.response;
    return {
      id: cred.id, rawId: fromBuf(cred.rawId), type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      response: {
        authenticatorData: fromBuf(r.authenticatorData),
        clientDataJSON: fromBuf(r.clientDataJSON),
        signature: fromBuf(r.signature),
        userHandle: r.userHandle ? fromBuf(r.userHandle) : undefined,
      },
      clientExtensionResults: cred.getClientExtensionResults(),
    };
  }

  // send makes one request to LADO and returns {status, body}, body being
  // the JSON answer or null. It throws only when no answer came.
  async function send(method, path, body) {
    const init = { method, cache: "no-store", credentials: "same-origin", headers: {} };
    if (body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetch(path, init);
    } catch (_) {
      throw new Error("LADO 의 답을 받지 못했습니다. 폰의 Tailscale 연결을 확인하세요.");
    }
    let data = null;
    try {
      data = await res.json();
    } catch (_) {
      data = null;
    }
    return { status: res.status, body: data };
  }

  // The words for a refused request the gate answers without JSON.
  const FORBIDDEN = "접근이 거절됐습니다. 폰 승인을 켠 Tailscale 계정으로 열었는지 확인하세요.";

  function credentialError(e) {
    switch (e && e.name) {
      case "NotAllowedError": return "취소했거나 시간이 지났습니다. 다시 누르세요.";
      case "AbortError": return "패스키 창을 닫았습니다.";
      case "InvalidStateError": return "이 폰의 패스키는 이미 등록되어 있습니다.";
      case "SecurityError": return "이 주소에서는 패스키를 쓸 수 없습니다. 데스크톱 앱이 보여 준 https 주소로 여세요.";
      default: return `패스키 창이 실패했습니다: ${(e && e.name) || "오류"}`;
    }
  }

  function supported() {
    return !!(window.PublicKeyCredential && navigator.credentials);
  }

  return Object.freeze({
    toBuf, fromBuf, creationOptions, requestOptions, attestationJSON, assertionJSON,
    send, credentialError, supported, FORBIDDEN,
  });
})();
