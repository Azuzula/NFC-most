// nfc-bridge.js – klient pro NFC most 1.3+ (čistý JavaScript, bez závislostí)
//
// NFC most je program na počítači s NFC čtečkou. Běží na jednom z portů
// 7878, 3535, 6262 a webové stránce zpřístupňuje čtečku přes WebSocket.
// Klient zkouší všechny porty, připojí se k tomu, kde odpoví NFC most,
// a když most neběží, zkouší to dokola.
//
//   import { NfcBridge } from "./nfc-bridge.js";
//   const nfc = new NfcBridge();
//   nfc.on("status", (s) => console.log(s.connected, s.permission, s.readerConnected));
//   nfc.on("card", (card) => console.log(card.uid, card.ndef));
//   const res = await nfc.writeTag({ url: "https://muj-web.cz/prukaz/abc" });
//
// card   = { uid, cardType, reader, time, initial, ndef: { formatted, writable, locked, protected, capacity, used, records } }
// result = { ok, uid, bytes, capacity, code, message }

export const NFC_BRIDGE_PORTS = [7878, 3535, 6262];

const INITIAL_STATUS = Object.freeze({
  connected: false,      // spojení s NFC mostem
  permission: "unknown", // unknown | pending (čeká se na potvrzení v okně mostu) | granted | denied
  canWrite: false,       // web smí číst i zapisovat
  readerConnected: false,
  readerName: "",
  version: "",
  port: null,
  writing: false,        // čeká se na přiložení tagu k zápisu
  lastCard: null,
});

export class NfcBridge {
  static initialStatus() { return { ...INITIAL_STATUS }; }

  constructor({ ports = NFC_BRIDGE_PORTS, host = "127.0.0.1", autoConnect = true } = {}) {
    this.ports = ports;
    this.host = host;
    this.status = NfcBridge.initialStatus();
    this._listeners = {};
    this._ws = null;
    this._probes = [];
    this._pending = new Map(); // id požadavku → { resolve, kind }
    this._seq = 0;
    this._stopped = true;
    this._retryTimer = null;
    this._delay = 1000;
    if (autoConnect) this.connect();
  }

  // ---------- události: status | card | removed | error ----------
  on(event, fn) {
    (this._listeners[event] ||= new Set()).add(fn);
    if (event === "status") fn({ ...this.status });
    return () => this._listeners[event]?.delete(fn);
  }
  _emit(event, data) { this._listeners[event]?.forEach((fn) => { try { fn(data); } catch (e) { console.error(e); } }); }
  _setStatus(patch) { this.status = { ...this.status, ...patch }; this._emit("status", { ...this.status }); }

  // ---------- spojení ----------
  connect() {
    if (!this._stopped) return;
    this._stopped = false;
    this._scan();
  }

  disconnect() {
    this._stopped = true;
    clearTimeout(this._retryTimer);
    this._closeProbes();
    if (this._ws) { const ws = this._ws; this._ws = null; ws.onclose = null; ws.close(); }
    this._failAll("disconnected", "Spojení s NFC mostem bylo ukončeno.");
    this._setStatus(NfcBridge.initialStatus());
  }

  _closeProbes(keep) {
    for (const ws of this._probes) if (ws !== keep) { ws.onclose = ws.onerror = ws.onmessage = null; try { ws.close(); } catch {} }
    this._probes = [];
  }

  _retry(ms) {
    if (this._stopped) return;
    clearTimeout(this._retryTimer);
    this._retryTimer = setTimeout(() => this._scan(), ms);
  }

  // Zkusí všechny porty najednou – vyhraje první, kde odpoví NFC most.
  _scan() {
    if (this._stopped || this._ws) return;
    this._closeProbes();
    let won = false;
    for (const port of this.ports) {
      let ws;
      try { ws = new WebSocket(`ws://${this.host}:${port}/ws`); } catch { continue; }
      this._probes.push(ws);
      ws.onmessage = (e) => {
        let m; try { m = JSON.parse(e.data); } catch { return; }
        if (won || m.type !== "hello" || m.app !== "nfc-most") { ws.close(); return; }
        won = true;
        clearTimeout(scanTimer);
        this._closeProbes(ws);
        this._adopt(ws, port, m);
      };
    }
    // když se do 3 s nikdo neozve, zkusíme to znovu později (čím dál méně často)
    const scanTimer = setTimeout(() => {
      if (won) return;
      this._closeProbes();
      this._retry(this._delay);
      this._delay = Math.min(this._delay * 2, 10000);
    }, 3000);
  }

  _adopt(ws, port, hello) {
    this._ws = ws;
    this._delay = 1000;
    this._setStatus({ connected: true, port, version: hello.version || "", permission: hello.permission || "granted", canWrite: !!hello.canWrite });
    ws.onmessage = (e) => { let m; try { m = JSON.parse(e.data); } catch { return; } this._handle(m); };
    ws.onclose = () => {
      this._ws = null;
      const denied = this.status.permission === "denied";
      this._failAll("disconnected", "Spojení s NFC mostem se přerušilo.");
      this._setStatus({ ...NfcBridge.initialStatus(), permission: denied ? "denied" : "unknown" });
      // zamítnutý web zkoušíme jen občas (uživatel může přístup povolit v nastavení mostu)
      this._retry(denied ? 30000 : 1000);
    };
  }

  _handle(m) {
    switch (m.type) {
      case "permission":
        this._setStatus({ permission: m.permission, canWrite: m.permission === "granted" });
        break;
      case "reader":
        this._setStatus({ readerConnected: !!m.connected, readerName: m.reader || "" });
        break;
      case "card": {
        const card = { uid: m.uid, cardType: m.cardType || "", reader: m.reader, time: m.time, initial: !!m.initial, ndef: m.ndef || null };
        this._setStatus({ lastCard: card });
        this._emit("card", card);
        break;
      }
      case "removed":
        this._emit("removed", { uid: m.uid, reader: m.reader, time: m.time });
        break;
      case "error":
        this._emit("error", m);
        break;
      case "write-result":
      case "measure-result":
        this._settle(m.id, m);
        break;
    }
  }

  // ---------- požadavky ----------
  _request(kind, payload) {
    return new Promise((resolve) => {
      const ws = this._ws;
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        resolve({ ok: false, code: "disconnected", message: "NFC most neběží." });
        return;
      }
      if (!this.status.canWrite) {
        resolve({ ok: false, code: "forbidden", message: this.status.permission === "pending"
          ? "Nejdřív potvrď přístup v okně NFC mostu." : "Tento web nemá povolený přístup k NFC mostu." });
        return;
      }
      const id = kind[0] + Date.now().toString(36) + "-" + (++this._seq);
      this._pending.set(id, { resolve, kind });
      if (kind === "write") this._setStatus({ writing: true });
      ws.send(JSON.stringify({ ...payload, type: kind, id }));
    });
  }

  _settle(id, result) {
    const p = this._pending.get(id);
    if (!p) return;
    this._pending.delete(id);
    if (p.kind === "write" && ![...this._pending.values()].some((x) => x.kind === "write")) this._setStatus({ writing: false });
    if (result.ok === undefined) result.ok = false;
    p.resolve(result);
  }

  _failAll(code, message) {
    for (const id of [...this._pending.keys()]) this._settle(id, { ok: false, code, message });
  }

  /**
   * Zapíše data na tag. Když tag leží na čtečce, zapíše se hned, jinak most
   * čeká na přiložení (nejdéle `timeout` sekund).
   * data: { url } | { text, lang } | { records: [{ type: "url", url }, { type: "text", text, lang }, …] }
   */
  writeTag(data, { timeout = 30 } = {}) { return this._request("write", { ...data, timeout }); }

  /** Zruší čekající zápis. */
  cancelWrite() {
    for (const [id, p] of this._pending) if (p.kind === "write") this._ws?.send(JSON.stringify({ type: "cancel", id }));
  }

  /** Spočítá, kolik bajtů by data na tagu zabrala (NTAG213 má 144 B). */
  measure(data) { return this._request("measure", data); }
}

// Zjistí, jestli se data vejdou na kartu (card.ndef z události "card").
// Vrací null, když zápis půjde, jinak text s důvodem.
export function writeBlockReason(card, bytes) {
  if (!card) return "Přilož tag ke čtečce.";
  const n = card.ndef;
  if (!n) return `Tenhle typ karty (${card.cardType || "neznámý"}) nejde zapisovat. Použij NTAG213, NTAG215 nebo NTAG216.`;
  if (!n.formatted) return "Tag není naformátovaný pro NDEF.";
  if (n.locked) return "Tag je natrvalo zamčený proti zápisu.";
  if (n.protected) return "Tag je chráněný heslem.";
  if (bytes != null && bytes > n.capacity) return `Data se nevejdou: potřeba ${bytes} B, tag má ${n.capacity} B.`;
  return null;
}

// UID ve sjednoceném tvaru: velká písmena, bez mezer a dvojteček.
export function normalizeUid(uid) {
  return String(uid || "").replace(/[^0-9a-f]/gi, "").toUpperCase();
}
