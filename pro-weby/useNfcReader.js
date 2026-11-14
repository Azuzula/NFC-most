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
