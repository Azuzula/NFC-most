---
name: nfc-most
description: Připojení webové aplikace k USB NFC čtečce přes lokální program „NFC most“ – čtení UID a obsahu NFC karet/tagů a zápis textu nebo odkazu na tagy NTAG. Použij, kdykoli má aplikace pracovat s NFC kartami, čipy nebo tagy přes čtečku připojenou k počítači.
---

# NFC most – práce s NFC čtečkou z webové aplikace

## Co to je

**NFC most** je malý program pro Windows, který běží na počítači s USB NFC čtečkou (např. ACR122U)
jako ikonka vedle hodin. Webové stránce zpřístupní čtečku přes WebSocket na adrese
`ws://127.0.0.1:<port>/ws`, kde port je **7878, 3535 nebo 6262** (most použije první volný).

- Když se k mostu poprvé připojí nový web, most se uživatele zeptá, jestli mu přístup povolit.
  Dokud uživatel nepotvrdí, je stav `permission: "pending"`.
- Most umí: číst UID každé karty, číst obsah NFC tagů (odkazy, text…) a zapisovat na tagy
  NTAG213 (144 B), NTAG215 (496 B), NTAG216 (872 B).
- Most běží jen na počítači se čtečkou. Na mobilu nebo jiném počítači nebude – aplikace musí
  fungovat i bez něj.

## Pravidla (dodržuj vždy)

1. **Nepiš vlastního WebSocket klienta.** Vytvoř soubor `nfc-bridge.js` přesně s kódem níže.
   V Reactu k němu přidej `useNfcReader.js` (také níže). Kód neupravuj.
2. **Počítej s tím, že most neběží.** Zbytek aplikace musí fungovat normálně; NFC prvky jsou doplněk.
3. **Vždy ukaž stav** (malý štítek u NFC funkcí):
   - `!connected` → „NFC most neběží“ (šedá)
   - `permission === "pending"` → „Potvrď přístup v okně NFC mostu“ (oranžová)
   - `permission === "denied"` → „Přístup zamítnut – povol ho v nastavení NFC mostu“ (červená)
   - `!readerConnected` → „Čtečka není připojená“ (oranžová)
   - jinak → „Čtečka připravena“ (zelená)
4. **Připojuj se jen na stránkách, kde je NFC potřeba** (hook/klient vytvářej v té stránce, ne globálně).
5. **UID** ukládej jako text a vždy ho prožeň `normalizeUid()` (velká písmena, bez oddělovačů).
   Jedno UID smí patřit jen jednomu záznamu. Pro karty je lepší samostatná entita
   (UID, vlastník, aktivní, datum přidání) než pole u uživatele.
6. **Zápis:** když tag leží na čtečce, zapíše se **okamžitě**, jinak most čeká na přiložení.
   Před zápisem ukaž, co se zapíše; během čekání (`writing === true`) ukaž výzvu
   „Přilož tag ke čtečce“ s tlačítkem Zrušit (`cancelWrite()`).
7. **Vždy ošetři výsledek zápisu:** `res.ok === false` → ukaž `res.message`.
8. `card.initial === true` znamená, že karta už ležela na čtečce, když se stránka připojila.
   U akcí, které se mají spustit jen po přiložení (např. výdej), takovou kartu ignoruj.
9. Chrome se může jednou zeptat na povolení „přístupu k zařízením v místní síti“ – to je v pořádku.

## API

```js
import { NfcBridge, normalizeUid, writeBlockReason } from "./nfc-bridge.js";
const nfc = new NfcBridge();                      // hned se začne připojovat
nfc.on("status", (s) => …);                       // s = { connected, permission, canWrite, readerConnected, readerName, writing, lastCard, port, version }
nfc.on("card", (card) => …);                      // přiložena karta
nfc.on("removed", ({ uid }) => …);                // karta odebrána
await nfc.writeTag({ url: "https://…" });         // zápis odkazu
await nfc.writeTag({ text: "…", lang: "cs" });    // zápis textu
await nfc.writeTag({ records: [ … ] }, { timeout: 30 });
nfc.cancelWrite();
await nfc.measure({ url: "https://…" });          // { ok, bytes } – kolik místa data zaberou
writeBlockReason(card, bytes);                    // null = zápis půjde, jinak text s důvodem
nfc.disconnect();
```

V Reactu: `const nfc = useNfcReader({ onCard, onRemoved })` vrací stejný stav jako `status`
plus funkce `writeTag`, `cancelWrite`, `measure`.

**Karta** (`card`):
```json
{ "uid": "04A23B1C5D6E80", "cardType": "MIFARE Ultralight / NTAG", "initial": false,
  "ndef": { "formatted": true, "writable": true, "locked": false, "protected": false,
            "capacity": 144, "used": 31,
            "records": [ { "type": "url", "url": "https://muj-web.cz" },
                         { "type": "text", "text": "Člen č. 7", "lang": "cs" } ] } }
```
`ndef` je `null` u karet, které nejsou NFC tagy (např. MIFARE Classic, bankovní karty) –
ty mají jen UID a zapisovat na ně nejde.

**Záznamy pro zápis** (`records`):
- `{ "type": "url", "url": "https://…" }` – funguje i `tel:+420…`, `mailto:…`, `sms:…`, `geo:50.1,16.4`
- `{ "type": "text", "text": "…", "lang": "cs" }`
- `{ "type": "mime", "mimeType": "text/vcard", "text": "BEGIN:VCARD…" }` (nebo `"data"` v base64)
- `{ "type": "aar", "package": "com.example.app" }` – Android otevře aplikaci

**Výsledek zápisu**: `{ ok, uid, bytes, capacity, code, message }`.
Kódy chyb: `forbidden` (web nemá přístup), `disconnected`, `invalid`, `too-large`, `timeout`,
`cancelled`, `replaced`, `locked`, `protected` (heslo), `not-ndef`, `unsupported` (typ karty),
`io` (tag odtržen), `verify`.

## Soubor `nfc-bridge.js`

```js
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
```

## Soubor `useNfcReader.js` (jen pro React)

```js
// useNfcReader.js – React hook pro NFC most 1.3+ (potřebuje soubor nfc-bridge.js vedle sebe)
//
//   const nfc = useNfcReader({ onCard: (card) => console.log(card.uid) });
//   nfc.connected, nfc.permission, nfc.readerConnected, nfc.canWrite, nfc.writing, nfc.lastCard
//   const res = await nfc.writeTag({ url: "https://muj-web.cz/prukaz/abc" });
//   if (!res.ok) alert(res.message);

import { useCallback, useEffect, useRef, useState } from "react";
import { NfcBridge } from "./nfc-bridge.js";

export { normalizeUid, writeBlockReason } from "./nfc-bridge.js";

const notRunning = () => Promise.resolve({ ok: false, code: "disconnected", message: "NFC most neběží." });

export function useNfcReader({ onCard, onRemoved, enabled = true, ports } = {}) {
  const [status, setStatus] = useState(NfcBridge.initialStatus);
  const bridgeRef = useRef(null);
  const handlers = useRef({});
  handlers.current = { onCard, onRemoved };
  const portsKey = (ports || []).join(",");

  useEffect(() => {
    if (!enabled) return;
    const bridge = new NfcBridge(ports ? { ports } : {});
    bridgeRef.current = bridge;
    const offs = [
      bridge.on("status", setStatus),
      bridge.on("card", (card) => handlers.current.onCard?.(card)),
      bridge.on("removed", (r) => handlers.current.onRemoved?.(r)),
    ];
    return () => {
      offs.forEach((off) => off());
      bridge.disconnect();
      bridgeRef.current = null;
      setStatus(NfcBridge.initialStatus());
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, portsKey]);

  const writeTag = useCallback((data, opts) => bridgeRef.current?.writeTag(data, opts) ?? notRunning(), []);
  const cancelWrite = useCallback(() => bridgeRef.current?.cancelWrite(), []);
  const measure = useCallback((data) => bridgeRef.current?.measure(data) ?? notRunning(), []);

  return { ...status, bridgeConnected: status.connected, writeTag, cancelWrite, measure };
}
```

## Typické úlohy

**Přiřazení karty uživateli** – tlačítko „Přiřadit kartu“ → výzva „Přilož kartu“ → v `onCard`
(ignoruj `card.initial`) zkontroluj, že `normalizeUid(card.uid)` ještě nikomu nepatří, a ulož ho.

**Vyhledání podle karty** – v `onCard` najdi záznam podle `normalizeUid(card.uid)`;
neznámou nebo deaktivovanou kartu oznam varováním.

**Zápis odkazu na průkaz** – odkaz má obsahovat náhodný kód (ne pořadové číslo ani jméno), protože
ho přečte kdokoli s telefonem:
```js
const res = await nfc.writeTag({ url: `https://muj-web.cz/prukaz/${member.publicCode}` });
if (res.ok) await saveCard({ uid: normalizeUid(res.uid), memberId: member.id });
else showError(res.message);
```

## Ukázka – čistý JavaScript

```html
<!doctype html>
<html lang="cs">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>NFC most – ukázka</title>
<!--
  Ukázka čtení a zápisu NFC tagů přes NFC most v čistém JavaScriptu.
  Soubor nfc-bridge.js musí ležet vedle této stránky.
  Stránku otevři přes webový server (nebo ji nahraj na svůj web) –
  přímo z disku (file://) prohlížeč JavaScriptové moduly nenačte.
-->
<style>
  body { font: 16px/1.5 system-ui, sans-serif; max-width: 560px; margin: 32px auto; padding: 0 16px; }
  .status { padding: 8px 12px; border-radius: 8px; background: #eee; }
  .ok { background: #dff3e7; } .warn { background: #fbefdf; } .bad { background: #f9e3e7; }
  code { font-size: 20px; }
  input { width: 100%; padding: 8px; font: inherit; box-sizing: border-box; }
  button { padding: 8px 16px; font: inherit; margin-top: 8px; }
</style>
</head>
<body>
  <h1>NFC most – ukázka</h1>
  <p class="status" id="status">Připojuji…</p>

  <h2>Čtení</h2>
  <p>Poslední karta: <code id="uid">—</code></p>
  <ul id="records"></ul>

  <h2>Zápis</h2>
  <label for="url">Odkaz, který se zapíše na tag</label>
  <input id="url" value="https://muj-web.cz/prukaz/abc123">
  <button id="write">Zapsat na tag</button>
  <button id="cancel" hidden>Zrušit</button>
  <p id="result"></p>

<script type="module">
  import { NfcBridge } from "./nfc-bridge.js";

  const $ = (id) => document.getElementById(id);
  const nfc = new NfcBridge();

  // stav spojení – vždy ukaž uživateli, co se děje
  nfc.on("status", (s) => {
    const el = $("status");
    if (!s.connected) { el.textContent = "NFC most neběží"; el.className = "status bad"; }
    else if (s.permission === "pending") { el.textContent = "Potvrď přístup v okně NFC mostu"; el.className = "status warn"; }
    else if (s.permission === "denied") { el.textContent = "Přístup zamítnut – povol ho v nastavení NFC mostu"; el.className = "status bad"; }
    else if (!s.readerConnected) { el.textContent = "Čtečka není připojená"; el.className = "status warn"; }
    else { el.textContent = "Čtečka připravena – přilož kartu"; el.className = "status ok"; }
    $("write").disabled = !s.canWrite || s.writing;
    $("cancel").hidden = !s.writing;
  });

  // přiložená karta
  nfc.on("card", (card) => {
    $("uid").textContent = card.uid;
    const list = $("records");
    list.replaceChildren();
    for (const r of card.ndef?.records || []) {
      const li = document.createElement("li");
      li.textContent = r.type === "url" ? r.url : r.type === "text" ? r.text : r.type;
      list.append(li);
    }
  });

  // zápis
  $("write").onclick = async () => {
    $("result").textContent = "Přilož tag ke čtečce…";
    const res = await nfc.writeTag({ url: $("url").value }, { timeout: 30 });
    $("result").textContent = res.ok ? `Zapsáno na ${res.uid} (${res.bytes} z ${res.capacity} B)` : `Chyba: ${res.message}`;
  };
  $("cancel").onclick = () => nfc.cancelWrite();
</script>
</body>
</html>
```

## Ukázka – React

```jsx
// Ukázka v Reactu: stav čtečky, poslední karta a zápis odkazu na tag.
// Potřebuje soubory useNfcReader.js a nfc-bridge.js ve stejné složce.

import { useState } from "react";
import { useNfcReader, normalizeUid } from "./useNfcReader.js";

function NfcStatus({ nfc }) {
  let text = "Čtečka připravena", color = "#2f9e6a";
  if (!nfc.connected) { text = "NFC most neběží"; color = "#8a8596"; }
  else if (nfc.permission === "pending") { text = "Potvrď přístup v okně NFC mostu"; color = "#d9821f"; }
  else if (nfc.permission === "denied") { text = "Přístup zamítnut"; color = "#c8475b"; }
  else if (!nfc.readerConnected) { text = "Čtečka není připojená"; color = "#d9821f"; }
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
      <span style={{ width: 10, height: 10, borderRadius: "50%", background: color }} />
      {text}
    </span>
  );
}

export default function NfcPriklad() {
  const [url, setUrl] = useState("https://muj-web.cz/prukaz/abc123");
  const [result, setResult] = useState("");

  const nfc = useNfcReader({
    onCard: (card) => {
      // card.initial = karta už ležela na čtečce, když se stránka připojila
      console.log("Přiložena karta", normalizeUid(card.uid), card.ndef?.records);
    },
  });

  async function write() {
    setResult("Přilož tag ke čtečce…");
    const res = await nfc.writeTag({ url }, { timeout: 30 });
    setResult(res.ok ? `Zapsáno na ${res.uid} (${res.bytes} z ${res.capacity} B)` : `Chyba: ${res.message}`);
  }

  return (
    <div style={{ maxWidth: 520, fontFamily: "system-ui, sans-serif" }}>
      <p><NfcStatus nfc={nfc} /></p>

      <h3>Poslední karta</h3>
      <p><code>{nfc.lastCard?.uid ?? "—"}</code></p>
      <ul>
        {(nfc.lastCard?.ndef?.records ?? []).map((r, i) => (
          <li key={i}>{r.type === "url" ? r.url : r.type === "text" ? r.text : r.type}</li>
        ))}
      </ul>

      <h3>Zápis odkazu</h3>
      <input value={url} onChange={(e) => setUrl(e.target.value)} style={{ width: "100%" }} />
      <button onClick={write} disabled={!nfc.canWrite || nfc.writing}>Zapsat na tag</button>
      {nfc.writing && <button onClick={nfc.cancelWrite}>Zrušit</button>}
      <p>{result}</p>
    </div>
  );
}
```
