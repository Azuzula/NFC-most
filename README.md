# NFC-most
Malá aplikace pro Windows, která propojí NFC/RFID čtečku s webem.

Co to dělá
----------
Čte UID a obsah NFC karet a tagů a umí na tagy zapisovat. Webové aplikace se
k němu připojí přes WebSocket. Běží jen jako ikonka v oznamovací oblasti
vedle hodin.

Spuštění
--------
1. Připoj čtečku do USB.
2. Spusť nfc-most.exe. Při prvním spuštění může Windows ukázat
   „Systém Windows ochránil váš počítač“ → Další informace → Přesto spustit.
3. Z menu ikonky otevři testovací stránku.

Tip: Windows nové ikonky často schová pod šipku „^“. Přetáhni ikonku
myší na lištu a zůstane vidět pořád.

Barva ikonky
------------
  šedá      – čtečka není připojená
  zelená    – čtečka je připravená
  oranžová  – čeká se na přiložení tagu k zápisu
  fialová   – právě se přečetla nebo zapsala karta (bliknutí)
  červená   – něco se nepovedlo (bliknutí)

Porty
-----
Most běží na prvním volném z portů 7878, 3535 a 6262. Webové aplikace
(klient nfc-bridge.js) zkoušejí všechny tři, takže se nic nenastavuje.
Testovací stránka je na http://localhost:7878 (případně 3535 / 6262 –
nejjednodušší je otevřít ji z menu ikonky).

Přístup webů
------------
Když se k mostu poprvé připojí nový web, most se zeptá:
„Web … chce používat NFC čtečku. Povolit?“
Odpověď si zapamatuje. Na testovací stránce v Nastavení je seznam
povolených a zamítnutých webů; web, který odebereš, se příště zeptá znovu.
Testovací stránka mostu smí vždy.

Testovací stránka
-----------------
- Přiložená karta: UID, typ, obsah tagu a zaplněnost.
- Zápis na tag: záznamy (text, odkaz, telefon, e-mail, SMS).
  Tlačítko ukazuje stav podle přiloženého tagu:
    „Přilož tag“     – na čtečce nic neleží
    „Zapsat na tag“  – tag je připravený
    (neaktivní)      – pod tlačítkem je důvod: nepodporovaný typ karty,
                       data se nevejdou, tag je zamčený, chráněný heslem…
  „Zapisovat automaticky na každý přiložený tag“ – pro vydávání více karet.
  „JSON pro tvůj web“ – přesná zpráva pro vlastní aplikaci.
- Presety: ulož si formulář pod názvem a příště ho jen vybereš.
- Nastavení: weby s přístupem; data (otevřít složku, export, mazání).

Data
----
Vše je ve složce %LOCALAPPDATA%\NFC most:
  config.json      povolené a zamítnuté weby
  nfc-most.log     záznam událostí
  presety\*.json   presety zápisu – soubory sem můžeš i nakopírovat,
                   testovací stránka je načte sama
Složku otevřeš z menu ikonky nebo na testovací stránce. Tam je i export
do zipu a mazání (presety, záznam událostí, nebo vše včetně složky –
v tom případě se most ukončí).

Kapacita tagů
-------------
  NTAG213   144 B   (odkaz zhruba do 130 znaků)
  NTAG215   496 B
  NTAG216   872 B
Most zapisuje na tagy NTAG21x a MIFARE Ultralight (NFC Forum Type 2).
Tagy chráněné heslem pozná, ale zapisovat na ně zatím neumí.

Pro vývojáře
------------
Složka pro-weby:
  NFC-MOST.md       návod („skill“) pro AI buildery i lidi – vlož ho do AI
                    builderu jako knowledge / instructions / rules
  PROMPT.txt        jak ho použít a ukázkový prompt
  nfc-bridge.js     klient v čistém JavaScriptu
  useNfcReader.js   React hook (potřebuje nfc-bridge.js)
  priklad.html      ukázka v čistém JavaScriptu
  NfcPriklad.jsx    ukázka v Reactu

Volba -demo
-----------
nfc-most.exe -demo  – bez čtečky, střídavě přikládá virtuální tagy
(odkaz, text, prázdný, zamčený, chráněný heslem, MIFARE Classic).
Hodí se na vyzkoušení webu.

Když to nefunguje
-----------------
- Ikonka zůstává šedá: zkus jiný USB port, případně nainstaluj ovladač
  ke tvé čtečce a zkontroluj, že ve Službách běží služba
  „Čipová karta“.
- „NFC most už běží“: hledej ikonku vedle hodin (případně pod šipkou „^“).
- Web hlásí „Přístup zamítnut“: na testovací stránce v Nastavení web odeber
  a při dalším připojení povol.
- Web se nepřipojí: Chrome se může zeptat na povolení přístupu
  k zařízením v místní síti – povol ho.
- Co se dělo, najdeš v záznamu událostí (menu → Otevřít záznam událostí).
