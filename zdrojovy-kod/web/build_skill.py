# Sestaví NFC-MOST.md ze šablony a aktuálních souborů klienta.
import pathlib
d = pathlib.Path(__file__).parent
t = (d / "NFC-MOST.template.md").read_text(encoding="utf-8")
for key, name in [("NFC_BRIDGE_JS", "nfc-bridge.js"), ("USE_NFC_READER_JS", "useNfcReader.js"),
                  ("PRIKLAD_HTML", "priklad.html"), ("NFC_PRIKLAD_JSX", "NfcPriklad.jsx")]:
    t = t.replace("{{" + key + "}}", (d / name).read_text(encoding="utf-8").rstrip())
import re; assert not re.search(r"\{\{[A-Z_]+\}\}", t)
(d / "NFC-MOST.md").write_text(t, encoding="utf-8")
print("NFC-MOST.md", len(t), "znaků")
