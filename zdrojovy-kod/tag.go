// Čtení a zápis NDEF na tagy typu 2 (NTAG213/215/216, MIFARE Ultralight).
// Paměť je rozdělená na stránky po 4 bajtech:
//
//	stránka 0–1  UID
//	stránka 2    UID + zámky (lock bytes)
//	stránka 3    Capability Container (E1 10 <velikost/8> <přístup>)
//	stránka 4…   data – sem patří NDEF
package main

import (
	"bytes"
	"errors"
	"fmt"
)

// tagIO – přístup k paměti tagu (skutečná čtečka nebo virtuální tag v demo režimu).
type tagIO interface {
	ReadPages(page int) ([]byte, error)    // 16 bajtů = 4 stránky
	WritePage(page int, data []byte) error // 4 bajty = 1 stránka
}

// NDEFInfo – co je na tagu, posílá se webu u zprávy "card".
type NDEFInfo struct {
	Formatted bool     `json:"formatted"`       // tag je připravený pro NDEF
	Writable  bool     `json:"writable"`        // lze zapisovat (není zamčený ani chráněný heslem)
	Locked    bool     `json:"locked"`          // natrvalo zamčený proti zápisu
	Protected bool     `json:"protected"`       // zápis chráněný heslem
	Capacity  int      `json:"capacity"`        // místo pro data v bajtech
	Used      int      `json:"used"`            // kolik zabírá současný obsah
	Records   []Record `json:"records"`         // záznamy (prázdné = tag je prázdný)
	Error     string   `json:"error,omitempty"` // když se obsah nepodařilo přečíst
}

type WriteResult struct {
	Capacity int
	Bytes    int
}

// WriteError – chyba zápisu s kódem, podle kterého může web reagovat.
type WriteError struct {
	Code    string // unsupported | not-ndef | locked | protected | too-large | io | verify
	Message string
}

func (e *WriteError) Error() string { return e.Message }

func werr(code, format string, a ...any) *WriteError {
	return &WriteError{Code: code, Message: fmt.Sprintf(format, a...)}
}

type tagHeader struct {
	cc        []byte // stránka 3
	capacity  int
	locked    bool
	protected bool
}

func (h *tagHeader) writable() bool { return !h.locked && !h.protected }

// Konfigurační stránka (CFG0) podle velikosti NTAG21x. Bajt 3 = AUTH0:
// od této stránky dál je zápis chráněný heslem (0xFF = ochrana vypnutá).
var ntagConfigPage = map[int]int{144: 0x29, 496: 0x83, 872: 0xE3}

func readHeader(t tagIO) (*tagHeader, error) {
	b, err := t.ReadPages(0)
	if err != nil {
		return nil, err
	}
	if len(b) < 16 {
		return nil, errors.New("tag vrátil málo dat")
	}
	h := &tagHeader{cc: b[12:16]}
	if h.cc[0] != 0xE1 {
		return h, nil // není naformátovaný pro NDEF
	}
	h.capacity = int(h.cc[2]) * 8
	lock := b[8:12] // stránka 2: bajty 2 a 3 jsou statické zámky
	staticLocked := lock[2]&0xF0 != 0 || lock[3] != 0
	h.locked = h.cc[3]&0x0F != 0 || staticLocked

	if cfg, ok := ntagConfigPage[h.capacity]; ok {
		if c, err := t.ReadPages(cfg); err == nil && len(c) >= 4 {
			auth0 := int(c[3])
			lastDataPage := 4 + h.capacity/4 - 1
			h.protected = auth0 <= lastDataPage
		}
	}
	return h, nil
}

// readNDEF přečte a rozloží obsah tagu. Chyby vrací uvnitř NDEFInfo.
func readNDEF(t tagIO) NDEFInfo {
	info := NDEFInfo{Records: []Record{}}
	h, err := readHeader(t)
	if err != nil {
		info.Error = "obsah tagu nelze přečíst: " + err.Error()
		return info
	}
	if h.capacity == 0 {
		return info
	}
	info.Formatted, info.Capacity = true, h.capacity
	info.Writable, info.Locked, info.Protected = h.writable(), h.locked, h.protected

	var area []byte
	for page := 4; len(area) < h.capacity; page += 4 {
		b, err := t.ReadPages(page)
		if err != nil {
			if len(area) == 0 {
				info.Error = "obsah tagu nelze přečíst: " + err.Error()
				return info
			}
			break
		}
		area = append(area, b...)
		if len(area) > h.capacity {
			area = area[:h.capacity]
		}
		msg, used, found, need := FindNDEFInTLV(area)
		if need > 0 {
			continue // TLV pokračuje dál, načteme další stránky
		}
		if !found {
			return info // prázdný tag
		}
		info.Used = min(used+1, h.capacity) // +1 za terminátor FE
		recs, err := ParseNDEF(msg)
		if recs != nil {
			info.Records = recs
		}
		if err != nil {
			info.Error = "obsah tagu je poškozený: " + err.Error()
		}
		return info
	}
	info.Error = "obsah tagu je useknutý"
	return info
}

// writeNDEF zapíše NDEF zprávu a ověří ji zpětným čtením.
func writeNDEF(t tagIO, msg []byte) (WriteResult, *WriteError) {
	h, err := readHeader(t)
	if err != nil {
		return WriteResult{}, werr("io", "Tag nelze přečíst: %v", err)
	}
	if h.capacity == 0 {
		return WriteResult{}, werr("not-ndef", "Tag není naformátovaný pro NDEF (chybí Capability Container).")
	}
	if h.locked {
		return WriteResult{}, werr("locked", "Tag je natrvalo zamčený proti zápisu.")
	}
	if h.protected {
		return WriteResult{}, werr("protected", "Tag je chráněný heslem – zápis na chráněné tagy zatím most neumí.")
	}
	tlv := WrapTLV(msg)
	res := WriteResult{Capacity: h.capacity, Bytes: len(tlv)}
	if len(tlv) > h.capacity {
		return res, werr("too-large", "Data se na tag nevejdou: potřeba %d B, na tagu je místo jen pro %d B.", len(tlv), h.capacity)
	}
	for len(tlv)%4 != 0 {
		tlv = append(tlv, 0x00)
	}

	// Postup podle NFC Forum: nejdřív délka 0, pak data, nakonec skutečná délka.
	// Kdyby se tag odtrhl uprostřed zápisu, zůstane prázdný, ne poškozený.
	first := append([]byte(nil), tlv[:4]...)
	if first[1] == 0xFF {
		first[2], first[3] = 0, 0
	} else {
		first[1] = 0
	}
	if err := t.WritePage(4, first); err != nil {
		return res, writeIOErr(err)
	}
	for i := 4; i < len(tlv); i += 4 {
		if err := t.WritePage(4+i/4, tlv[i:i+4]); err != nil {
			return res, writeIOErr(err)
		}
	}
	if err := t.WritePage(4, tlv[:4]); err != nil {
		return res, writeIOErr(err)
	}

	// kontrola
	var back []byte
	for page := 4; len(back) < len(tlv); page += 4 {
		b, err := t.ReadPages(page)
		if err != nil {
			return res, werr("verify", "Zapsáno, ale kontrolní čtení selhalo: %v", err)
		}
		back = append(back, b...)
	}
	if !bytes.Equal(back[:len(tlv)], tlv) {
		return res, werr("verify", "Kontrola zápisu nesouhlasí – zkus to znovu a drž tag u čtečky.")
	}
	return res, nil
}

func writeIOErr(err error) *WriteError {
	return werr("io", "Zápis se nepovedl (%v). Drž tag u čtečky, dokud zápis neskončí. Pokud se to opakuje, tag může být chráněný heslem.", err)
}

// ---------- virtuální NTAG213 pro demo režim a testy ----------

type memTag struct {
	mem [45 * 4]byte
}

func newMemTag(uid []byte) *memTag {
	t := &memTag{}
	copy(t.mem[0:3], uid[:3])
	t.mem[3] = 0x88 ^ uid[0] ^ uid[1] ^ uid[2]
	copy(t.mem[4:8], uid[3:7])
	t.mem[8] = uid[3] ^ uid[4] ^ uid[5] ^ uid[6]
	copy(t.mem[12:16], []byte{0xE1, 0x10, 0x12, 0x00}) // CC: NDEF, 144 B, zápis povolen
	copy(t.mem[16:20], []byte{0x03, 0x00, 0xFE, 0x00}) // prázdná NDEF zpráva
	t.mem[0x29*4+3] = 0xFF                             // AUTH0: bez hesla
	return t
}

func (t *memTag) ReadPages(page int) ([]byte, error) {
	if page < 0 || page >= 45 {
		return nil, errors.New("stránka mimo rozsah")
	}
	out := make([]byte, 16)
	for i := 0; i < 16; i++ { // NTAG při čtení přes konec pokračuje od začátku
		out[i] = t.mem[(page*4+i)%len(t.mem)]
	}
	return out, nil
}

func (t *memTag) WritePage(page int, data []byte) error {
	if page < 4 || page > 39 || len(data) != 4 || page >= int(t.mem[0x29*4+3]) {
		return errors.New("zápis odmítnut")
	}
	copy(t.mem[page*4:], data)
	return nil
}
