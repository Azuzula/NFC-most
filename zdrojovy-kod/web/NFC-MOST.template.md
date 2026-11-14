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
{{NFC_BRIDGE_JS}}
```

## Soubor `useNfcReader.js` (jen pro React)

```js
{{USE_NFC_READER_JS}}
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
{{PRIKLAD_HTML}}
```

## Ukázka – React

```jsx
{{NFC_PRIKLAD_JSX}}
```
