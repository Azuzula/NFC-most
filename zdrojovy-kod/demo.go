// Demo režim – bez čtečky. Střídavě "přikládá" virtuální tagy, takže jde
// vyzkoušet čtení, zápis i hlášky u tagů, na které zapsat nejde.
package main

import (
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type demoTag struct {
	uid      string
	cardType string
	mem      *memTag // nil = karta bez NDEF paměti (MIFARE Classic)
}

func demoTags() []demoTag {
	mk := func(uidHex string, recs []Record) demoTag {
		uid, _ := hex.DecodeString(uidHex)
		t := newMemTag(uid)
		if recs != nil {
			msg, _ := EncodeNDEF(recs)
			writeNDEF(t, msg)
		}
		return demoTag{strings.ToUpper(uidHex), "MIFARE Ultralight / NTAG", t}
	}
	locked := mk("04B1A5ED000001", []Record{{Type: "text", Text: "Zamčený tag", Lang: "cs"}})
	locked.mem.mem[15] = 0x0F // CC: zápis zakázán
	protected := mk("04B1A5ED000002", []Record{{Type: "url", URL: "https://example.com/chraneny"}})
	protected.mem.mem[0x29*4+3] = 0x04 // AUTH0: heslo od stránky 4

	return []demoTag{
		mk("04A23B1C5D6E80", []Record{{Type: "url", URL: "https://example.com"}}),
		mk("04F19A2B3C4D81", []Record{{Type: "text", Text: "Ahoj z NFC mostu", Lang: "cs"}}),
		mk("04C0FFEE123456", nil), // prázdný
		locked,
		protected,
		{uid: "8A3F21C4", cardType: "MIFARE Classic 1K"},
	}
}

func demoLoop(hub *Hub) {
	const reader = "DEMO čtečka"
	tags := demoTags()

	var mu sync.Mutex
	var current *demoTag
	wake := make(chan struct{}, 1)
	hub.SetWaker(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	hub.SetReader(reader, true)

	process := func(t *demoTag, arrival bool) {
		var io tagIO
		if t.mem != nil {
			io = t.mem
		}
		hub.processCard(io, cardInfo{t.uid, t.cardType, reader}, arrival)
	}

	place := time.NewTimer(2 * time.Second)
	remove := time.NewTimer(time.Hour)
	remove.Stop()
	i := 0
	for {
		select {
		case <-place.C:
			mu.Lock()
			t := tags[i%len(tags)]
			i++
			current = &t
			process(current, true)
			mu.Unlock()
			remove.Reset(5 * time.Second)

		case <-remove.C:
			mu.Lock()
			uid := current.uid
			current = nil
			mu.Unlock()
			hub.Broadcast(Message{Type: "removed", UID: uid, Reader: reader})
			place.Reset(2 * time.Second)

		case <-wake:
			mu.Lock()
			if current != nil && hub.HasJob() {
				process(current, false)
			}
			mu.Unlock()
		}
	}
}
