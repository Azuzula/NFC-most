// Skutečná čtečka přes PC/SC (Windows: winscard.dll).
package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ebfe/scard"
)

// PC/SC pseudo-APDU (ACR122U i většina dalších čteček)
var apduGetUID = []byte{0xFF, 0xCA, 0x00, 0x00, 0x00}

// pcscTag – paměť tagu přes příkazy Read Binary / Update Binary.
type pcscTag struct{ card *scard.Card }

func (t pcscTag) ReadPages(page int) ([]byte, error) {
	resp, err := t.card.Transmit([]byte{0xFF, 0xB0, 0x00, byte(page), 0x10})
	if err != nil {
		return nil, err
	}
	if !swOK(resp) || len(resp) < 18 {
		return nil, fmt.Errorf("čtení stránky %d odmítnuto (%X)", page, resp)
	}
	return resp[:16], nil
}

func (t pcscTag) WritePage(page int, data []byte) error {
	cmd := append([]byte{0xFF, 0xD6, 0x00, byte(page), 0x04}, data...)
	resp, err := t.card.Transmit(cmd)
	if err != nil {
		return err
	}
	if !swOK(resp) {
		return fmt.Errorf("zápis stránky %d odmítnut (%X)", page, resp)
	}
	return nil
}

func swOK(resp []byte) bool {
	return len(resp) >= 2 && resp[len(resp)-2] == 0x90 && resp[len(resp)-1] == 0x00
}

// Odhad typu karty z ATR podle PC/SC part 3 (jak ho skládá ACR122U).
func cardTypeFromATR(atr []byte) string {
	if len(atr) < 15 || atr[4] != 0x80 || atr[5] != 0x4F {
		return ""
	}
	switch uint16(atr[13])<<8 | uint16(atr[14]) {
	case 0x0001:
		return "MIFARE Classic 1K"
	case 0x0002:
		return "MIFARE Classic 4K"
	case 0x0003:
		return "MIFARE Ultralight / NTAG"
	case 0x0026:
		return "MIFARE Mini"
	case 0x003A:
		return "MIFARE Ultralight C"
	case 0xF004:
		return "Topaz / Jewel"
	case 0xF011:
		return "FeliCa 212K"
	case 0xF012:
		return "FeliCa 424K"
	}
	return ""
}

// handleCard se připojí ke kartě, přečte UID a předá ji hubu.
func handleCard(ctx *scard.Context, hub *Hub, reader string, arrival bool) (uid string, err error) {
	card, err := ctx.Connect(reader, scard.ShareShared, scard.ProtocolAny)
	if err != nil {
		return "", fmt.Errorf("nelze se připojit ke kartě: %w", err)
	}
	defer card.Disconnect(scard.LeaveCard)

	cardType := ""
	if st, err := card.Status(); err == nil {
		cardType = cardTypeFromATR(st.Atr)
	}
	resp, err := card.Transmit(apduGetUID)
	if err != nil {
		return "", fmt.Errorf("chyba při čtení UID: %w", err)
	}
	if !swOK(resp) || len(resp) < 3 {
		return "", fmt.Errorf("karta nevrátila UID (odpověď %X)", resp)
	}
	uid = strings.ToUpper(hex.EncodeToString(resp[:len(resp)-2]))
	hub.processCard(pcscTag{card}, cardInfo{uid, cardType, reader}, arrival)
	return uid, nil
}

func readerLoop(hub *Hub) {
	for {
		ctx, err := scard.EstablishContext()
		if err != nil {
			hub.SetReader("", false)
			time.Sleep(2 * time.Second)
			continue
		}
		hub.SetWaker(func() { ctx.Cancel() })
		watchReaders(ctx, hub)
		hub.SetWaker(nil)
		ctx.Release()
		time.Sleep(1 * time.Second)
	}
}

func watchReaders(ctx *scard.Context, hub *Hub) {
	known := map[string]scard.StateFlag{} // poslední známý stav každé čtečky
	present := map[string]string{}        // čtečka -> UID karty, která na ní leží

	for {
		readers, err := ctx.ListReaders()
		if err != nil || len(readers) == 0 {
			hub.SetReader("", false)
			if err != nil && !errors.Is(err, scard.ErrNoReadersAvailable) {
				return // služba se zastavila apod. – založíme nový kontext
			}
			time.Sleep(1 * time.Second)
			continue
		}
		hub.SetReader(strings.Join(readers, ", "), true)

		for r := range known {
			if !contains(readers, r) {
				delete(known, r)
				delete(present, r)
			}
		}

		// Požadavek na zápis a karta už leží na čtečce → zapíšeme hned.
		if hub.HasJob() {
			for _, r := range readers {
				if _, ok := present[r]; ok {
					if _, err := handleCard(ctx, hub, r, false); err != nil {
						hub.FailJob("io", "Kartu na čtečce nelze přečíst: "+err.Error())
					}
					break
				}
			}
		}

		rs := make([]scard.ReaderState, len(readers))
		for i, r := range readers {
			rs[i] = scard.ReaderState{Reader: r, CurrentState: known[r]}
		}

		err = ctx.GetStatusChange(rs, 1*time.Second)
		if errors.Is(err, scard.ErrTimeout) || errors.Is(err, scard.ErrCancelled) {
			continue
		}
		if err != nil {
			return
		}

		for _, s := range rs {
			if s.EventState&scard.StateChanged == 0 {
				continue
			}
			known[s.Reader] = s.EventState &^ scard.StateChanged

			isPresent := s.EventState&scard.StatePresent != 0
			lastUID, wasPresent := present[s.Reader]

			switch {
			case isPresent && !wasPresent:
				uid, err := handleCard(ctx, hub, s.Reader, true)
				if err != nil {
					log.Printf("Karta přiložena, ale nepodařilo se ji přečíst: %v", err)
					hub.Broadcast(Message{Type: "error", Reader: s.Reader, Message: err.Error()})
				}
				present[s.Reader] = uid

			case !isPresent && wasPresent:
				delete(present, s.Reader)
				if lastUID != "" {
					log.Printf("Karta odebrána: %s", lastUID)
				}
				hub.Broadcast(Message{Type: "removed", UID: lastUID, Reader: s.Reader})
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
