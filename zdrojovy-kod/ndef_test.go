package main

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func TestEncodeKnownVectors(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want string
	}{
		// https://www.google.com → zkratka 0x02 + "google.com"
		{"url www", Record{Type: "url", URL: "https://www.google.com"}, "D1 01 0B 55 02 676F6F676C652E636F6D"},
		{"url https", Record{Type: "url", URL: "https://abc.cz"}, "D1 01 07 55 04 6162632E637A"},
		{"tel", Record{Type: "url", URL: "tel:+420123"}, "D1 01 08 55 05 2B343230313233"},
		{"vlastní schéma", Record{Type: "url", URL: "geo:50.1,16.4"}, "D1 01 0E 55 00 67656F3A35302E312C31362E34"},
		// text "Hello" v angličtině
		{"text en", Record{Type: "text", Text: "Hello", Lang: "en"}, "D1 01 08 54 02 656E 48656C6C6F"},
		{"aar", Record{Type: "aar", Package: "a.b"}, "D4 0F 03 616E64726F69642E636F6D3A706B67 612E62"},
	}
	for _, c := range cases {
		got, err := EncodeNDEF([]Record{c.rec})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !bytes.Equal(got, mustHex(c.want)) {
			t.Errorf("%s:\n got  % X\n want % X", c.name, got, mustHex(c.want))
		}
	}
}

func TestRoundTrip(t *testing.T) {
	in := []Record{
		{Type: "url", URL: "https://muj-web.cz/prukaz/k7Qm2xPa"},
		{Type: "text", Text: "Příliš žluťoučký kůň úpěl ďábelské ódy", Lang: "cs"},
		{Type: "mime", MimeType: "text/vcard", Text: "BEGIN:VCARD\nVERSION:3.0\nFN:Zuzana\nEND:VCARD"},
		{Type: "aar", Package: "com.wakdev.wdnfc"},
		{Type: "mime", MimeType: "application/octet-stream", Data: "AAECAwQ="},
		{Type: "text", Text: strings.Repeat("dlouhý text ", 30), Lang: "cs"}, // > 255 B → dlouhý záznam
	}
	msg, err := EncodeNDEF(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseNDEF(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("roundtrip mismatch\n in  %+v\n out %+v", in, out)
	}
}

func TestTLV(t *testing.T) {
	msg := mustHex("D1 01 08 55 04 636F6F7A2E637A")
	tlv := WrapTLV(msg)
	if !bytes.Equal(tlv[:2], []byte{0x03, byte(len(msg))}) || tlv[len(tlv)-1] != 0xFE {
		t.Fatalf("bad TLV % X", tlv)
	}
	// Lock Control TLV před NDEF blokem musí být přeskočen
	area := append(mustHex("01 03 A0 0C 34"), tlv...)
	area = append(area, 0, 0, 0)
	got, used, found, need := FindNDEFInTLV(area)
	if !found || need != 0 || !bytes.Equal(got, msg) || used != 5+len(tlv)-1 {
		t.Fatalf("found=%v need=%d used=%d got % X", found, need, used, got)
	}
	// useknutá data → je potřeba načíst víc
	if _, _, found, need := FindNDEFInTLV(tlv[:6]); found || need <= 0 {
		t.Fatalf("truncated: found=%v need=%d", found, need)
	}
	// dlouhý tvar délky
	big := make([]byte, 300)
	tlv = WrapTLV(big)
	if tlv[1] != 0xFF {
		t.Fatal("expected 3-byte length")
	}
	got, _, found, _ = FindNDEFInTLV(tlv)
	if !found || len(got) != 300 {
		t.Fatal("long TLV not parsed")
	}
	// prázdný naformátovaný tag
	if _, _, found, _ := FindNDEFInTLV(mustHex("03 00 FE 00")); !found {
		t.Fatal("empty NDEF not found")
	}
}

func TestEncodeErrors(t *testing.T) {
	bad := [][]Record{
		nil,
		{{Type: "url"}},
		{{Type: "nic"}},
		{{Type: "mime", Text: "x"}},
		{{Type: "mime", MimeType: "a/b", Data: "%%%"}},
	}
	for _, r := range bad {
		if _, err := EncodeNDEF(r); err == nil {
			t.Errorf("expected error for %+v", r)
		}
	}
}

func TestMemTagWriteRead(t *testing.T) {
	tag := newMemTag([]byte{0x04, 1, 2, 3, 4, 5, 6})
	recs := []Record{{Type: "url", URL: "https://muj-web.cz"}, {Type: "text", Text: "Člen č. 7", Lang: "cs"}}
	msg, _ := EncodeNDEF(recs)
	res, werr := writeNDEF(tag, msg)
	if werr != nil {
		t.Fatal(werr)
	}
	if res.Capacity != 144 {
		t.Fatalf("capacity %d", res.Capacity)
	}
	info := readNDEF(tag)
	if !info.Formatted || !info.Writable || !reflect.DeepEqual(info.Records, recs) {
		t.Fatalf("read back %+v", info)
	}

	// moc velká zpráva
	big, _ := EncodeNDEF([]Record{{Type: "text", Text: strings.Repeat("x", 200)}})
	if _, werr := writeNDEF(tag, big); werr == nil || werr.Code != "too-large" {
		t.Fatalf("expected too-large, got %v", werr)
	}
	// chráněný heslem od stránky 4
	tag.mem[0x29*4+3] = 0x04
	if info := readNDEF(tag); !info.Protected || info.Writable {
		t.Fatalf("expected protected, got %+v", info)
	}
	if _, werr := writeNDEF(tag, msg); werr == nil || werr.Code != "protected" {
		t.Fatalf("expected protected, got %v", werr)
	}
	tag.mem[0x29*4+3] = 0xFF
	// zamčený tag
	tag.mem[15] = 0x0F
	if _, werr := writeNDEF(tag, msg); werr == nil || werr.Code != "locked" {
		t.Fatalf("expected locked, got %v", werr)
	}
}
