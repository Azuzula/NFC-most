// NDEF – formát dat na NFC tagech, kterému rozumí telefony.
// Tady je převod mezi JSON záznamy (Record) a bajty NDEF zprávy.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Record je jeden NDEF záznam v podobě, jakou posílá a přijímá web.
//
//	{"type":"text", "text":"Ahoj", "lang":"cs"}
//	{"type":"url",  "url":"https://muj-web.cz"}        (i tel:, mailto:, sms:, geo: …)
//	{"type":"mime", "mimeType":"text/vcard", "text":"BEGIN:VCARD…"}  nebo "data" v base64
//	{"type":"aar",  "package":"com.example.app"}       (Android: otevře aplikaci)
type Record struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Lang       string `json:"lang,omitempty"`
	URL        string `json:"url,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Data       string `json:"data,omitempty"` // base64
	Package    string `json:"package,omitempty"`
	RecordType string `json:"recordType,omitempty"` // u neznámých záznamů
	TNF        int    `json:"tnf,omitempty"`        // u neznámých záznamů
}

// Typy záznamů, které most umí zapsat.
var writableTypes = []string{"text", "url", "mime", "aar"}

const (
	tnfEmpty     = 0x00
	tnfWellKnown = 0x01
	tnfMime      = 0x02
	tnfAbsURI    = 0x03
	tnfExternal  = 0x04
	tnfUnknown   = 0x05
)

// Zkratky na začátku URL podle NFC Forum URI RTD – ušetří místo na tagu.
var uriPrefixes = []string{
	"", "http://www.", "https://www.", "http://", "https://", "tel:", "mailto:",
	"ftp://anonymous:anonymous@", "ftp://ftp.", "ftps://", "sftp://", "smb://",
	"nfs://", "ftp://", "dav://", "news:", "telnet://", "imap:", "rtsp://", "urn:",
	"pop:", "sip:", "sips:", "tftp:", "btspp://", "btl2cap://", "btgoep://",
	"tcpobex://", "irdaobex://", "file://", "urn:epc:id:", "urn:epc:tag:",
	"urn:epc:pat:", "urn:epc:raw:", "urn:epc:", "urn:nfc:",
}

// ---------- zápis ----------

type rawRecord struct {
	tnf     byte
	typ     []byte
	payload []byte
}

func recordToRaw(r Record) (rawRecord, error) {
	switch strings.ToLower(r.Type) {
	case "text":
		lang := r.Lang
		if lang == "" {
			lang = "cs"
		}
		if len(lang) > 63 {
			return rawRecord{}, errors.New("kód jazyka je moc dlouhý")
		}
		if !utf8.ValidString(r.Text) {
			return rawRecord{}, errors.New("text není platné UTF-8")
		}
		p := append([]byte{byte(len(lang))}, lang...)
		p = append(p, r.Text...)
		return rawRecord{tnfWellKnown, []byte("T"), p}, nil

	case "url", "uri":
		u := strings.TrimSpace(r.URL)
		if u == "" {
			return rawRecord{}, errors.New("chybí URL")
		}
		code, rest := 0, u
		for i := 1; i < len(uriPrefixes); i++ { // nejdelší shoda vyhrává
			p := uriPrefixes[i]
			if strings.HasPrefix(strings.ToLower(u), p) && len(p) > len(uriPrefixes[code]) {
				code, rest = i, u[len(p):]
			}
		}
		return rawRecord{tnfWellKnown, []byte("U"), append([]byte{byte(code)}, rest...)}, nil

	case "mime":
		if r.MimeType == "" {
			return rawRecord{}, errors.New("chybí mimeType")
		}
		var payload []byte
		if r.Data != "" {
			b, err := base64.StdEncoding.DecodeString(r.Data)
			if err != nil {
				return rawRecord{}, errors.New("data nejsou platné base64")
			}
			payload = b
		} else {
			payload = []byte(r.Text)
		}
		return rawRecord{tnfMime, []byte(r.MimeType), payload}, nil

	case "aar":
		if r.Package == "" {
			return rawRecord{}, errors.New("chybí název balíčku (package)")
		}
		return rawRecord{tnfExternal, []byte("android.com:pkg"), []byte(r.Package)}, nil
	}
	return rawRecord{}, fmt.Errorf("neznámý typ záznamu %q (umím: %s)", r.Type, strings.Join(writableTypes, ", "))
}

// EncodeNDEF složí záznamy do jedné NDEF zprávy.
func EncodeNDEF(records []Record) ([]byte, error) {
	if len(records) == 0 {
		return nil, errors.New("není co zapsat – chybí záznamy")
	}
	var out []byte
	for i, r := range records {
		raw, err := recordToRaw(r)
		if err != nil {
			return nil, fmt.Errorf("záznam %d: %w", i+1, err)
		}
		if len(raw.typ) > 255 {
			return nil, fmt.Errorf("záznam %d: typ je moc dlouhý", i+1)
		}
		h := raw.tnf
		if i == 0 {
			h |= 0x80 // MB – první záznam
		}
		if i == len(records)-1 {
			h |= 0x40 // ME – poslední záznam
		}
		short := len(raw.payload) < 256
		if short {
			h |= 0x10 // SR – krátký záznam
		}
		out = append(out, h, byte(len(raw.typ)))
		if short {
			out = append(out, byte(len(raw.payload)))
		} else {
			n := len(raw.payload)
			out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		}
		out = append(out, raw.typ...)
		out = append(out, raw.payload...)
	}
	return out, nil
}

// WrapTLV zabalí NDEF zprávu do TLV bloku pro tagy typu 2 (NTAG, Ultralight).
func WrapTLV(msg []byte) []byte {
	var out []byte
	if len(msg) < 0xFF {
		out = []byte{0x03, byte(len(msg))}
	} else {
		out = []byte{0x03, 0xFF, byte(len(msg) >> 8), byte(len(msg))}
	}
	out = append(out, msg...)
	return append(out, 0xFE) // terminátor
}

// ---------- čtení ----------

// ParseNDEF rozloží NDEF zprávu na záznamy.
func ParseNDEF(b []byte) ([]Record, error) {
	var recs []Record
	for i := 0; i < len(b); {
		h := b[i]
		tnf := h & 0x07
		short := h&0x10 != 0
		hasID := h&0x08 != 0
		if h&0x20 != 0 {
			return recs, errors.New("dělené (chunked) záznamy nejsou podporované")
		}
		i++
		if i >= len(b) {
			return recs, errors.New("useknutý záznam")
		}
		typeLen := int(b[i])
		i++
		var payLen int
		if short {
			if i >= len(b) {
				return recs, errors.New("useknutý záznam")
			}
			payLen = int(b[i])
			i++
		} else {
			if i+4 > len(b) {
				return recs, errors.New("useknutý záznam")
			}
			payLen = int(b[i])<<24 | int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
			i += 4
		}
		idLen := 0
		if hasID {
			if i >= len(b) {
				return recs, errors.New("useknutý záznam")
			}
			idLen = int(b[i])
			i++
		}
		if payLen < 0 || i+typeLen+idLen+payLen > len(b) {
			return recs, errors.New("useknutý záznam")
		}
		typ := b[i : i+typeLen]
		i += typeLen + idLen
		payload := b[i : i+payLen]
		i += payLen

		recs = append(recs, decodeRecord(tnf, typ, payload))
		if h&0x40 != 0 { // ME
			break
		}
	}
	return recs, nil
}

func decodeRecord(tnf byte, typ, payload []byte) Record {
	switch {
	case tnf == tnfWellKnown && string(typ) == "T" && len(payload) >= 1:
		status := payload[0]
		ll := int(status & 0x3F)
		if 1+ll <= len(payload) {
			lang := string(payload[1 : 1+ll])
			body := payload[1+ll:]
			var text string
			if status&0x80 != 0 { // UTF-16
				text = decodeUTF16(body)
			} else {
				text = string(body)
			}
			return Record{Type: "text", Text: text, Lang: lang}
		}
	case tnf == tnfWellKnown && string(typ) == "U" && len(payload) >= 1:
		prefix := ""
		if int(payload[0]) < len(uriPrefixes) {
			prefix = uriPrefixes[payload[0]]
		}
		return Record{Type: "url", URL: prefix + string(payload[1:])}
	case tnf == tnfAbsURI:
		return Record{Type: "url", URL: string(typ)}
	case tnf == tnfMime:
		r := Record{Type: "mime", MimeType: string(typ)}
		mt := strings.ToLower(string(typ))
		if utf8.Valid(payload) && (strings.HasPrefix(mt, "text/") || strings.Contains(mt, "json") || strings.Contains(mt, "xml")) {
			r.Text = string(payload)
		} else {
			r.Data = base64.StdEncoding.EncodeToString(payload)
		}
		return r
	case tnf == tnfExternal && string(typ) == "android.com:pkg":
		return Record{Type: "aar", Package: string(payload)}
	case tnf == tnfEmpty:
		return Record{Type: "empty"}
	}
	return Record{Type: "unknown", TNF: int(tnf), RecordType: string(typ), Data: base64.StdEncoding.EncodeToString(payload)}
}

func decodeUTF16(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	be := true
	if b[0] == 0xFF && b[1] == 0xFE {
		be, b = false, b[2:]
	} else if b[0] == 0xFE && b[1] == 0xFF {
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if be {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(u))
}

// FindNDEFInTLV najde NDEF zprávu v datové oblasti tagu typu 2.
// Vrací zprávu, kde NDEF blok končí (bez terminátoru) a jestli byl nalezen.
// needMore > 0 znamená, že data jsou useknutá a je potřeba načíst víc bajtů.
func FindNDEFInTLV(area []byte) (msg []byte, used int, found bool, needMore int) {
	for i := 0; i < len(area); {
		t := area[i]
		switch t {
		case 0x00: // NULL TLV
			i++
			continue
		case 0xFE: // terminátor
			return nil, i + 1, false, 0
		}
		if i+1 >= len(area) {
			return nil, 0, false, i + 4 - len(area)
		}
		l := int(area[i+1])
		hdr := 2
		if l == 0xFF {
			if i+3 >= len(area) {
				return nil, 0, false, i + 4 - len(area)
			}
			l = int(area[i+2])<<8 | int(area[i+3])
			hdr = 4
		}
		end := i + hdr + l
		if end > len(area) {
			return nil, 0, false, end - len(area)
		}
		if t == 0x03 {
			return area[i+hdr : end], end, true, 0
		}
		i = end // Lock / Memory Control TLV apod. přeskočíme
	}
	return nil, 0, false, 0
}
