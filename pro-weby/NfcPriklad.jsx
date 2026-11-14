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
