package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// ---------- zprávy ----------

// Message – vše, co most posílá webu (JSON). Nepoužitá pole se neposílají.
type Message struct {
	Type       string    `json:"type"`
	ID         string    `json:"id,omitempty"`         // u odpovědí na požadavek webu
	App        string    `json:"app,omitempty"`        // u hello: vždy "nfc-most"
	Version    string    `json:"version,omitempty"`    // u hello
	CanWrite   *bool     `json:"canWrite,omitempty"`   // u hello: smí tento web zapisovat?
	Permission string    `json:"permission,omitempty"` // u hello/permission: granted | pending | denied
	Features   []string  `json:"features,omitempty"`   // u hello
	UID        string    `json:"uid,omitempty"`        // UID karty, hex velkými písmeny
	CardType   string    `json:"cardType,omitempty"`   // odhad typu karty
	Reader     string    `json:"reader,omitempty"`     // název čtečky
	Connected  *bool     `json:"connected,omitempty"`  // u type=reader
	NDEF       *NDEFInfo `json:"ndef,omitempty"`       // obsah tagu u type=card
	OK         *bool     `json:"ok,omitempty"`         // u výsledků
	Code       string    `json:"code,omitempty"`       // kód chyby
	Message    string    `json:"message,omitempty"`    // text chyby / informace
	Bytes      int       `json:"bytes,omitempty"`      // velikost zapisovaných dat
	Capacity   int       `json:"capacity,omitempty"`   // kapacita tagu
	Records    []Record  `json:"records,omitempty"`    // zapsané záznamy
	Initial    bool      `json:"initial,omitempty"`    // u card: karta už ležela na čtečce, když se web připojil
	Time       string    `json:"time"`
}

func now() string       { return time.Now().Format(time.RFC3339) }
func ptr[T any](v T) *T { return &v }

// ---------- připojený web ----------

type client struct {
	send     chan []byte
	origin   string
	canWrite bool
}

func (c *client) Send(m Message) {
	if m.Time == "" {
		m.Time = now()
	}
	b, _ := json.Marshal(m)
	select {
	case c.send <- b:
	default:
	}
}

// ---------- požadavek na zápis ----------

type writeJob struct {
	id      string
	client  *client
	records []Record
	msg     []byte // hotová NDEF zpráva
	timer   *time.Timer
}

// ---------- hub ----------

type Hub struct {
	mu        sync.Mutex
	clients   map[*client]struct{}
	readerMsg Message
	present   map[string]Message // čtečka → karta, která na ní právě leží
	job       *writeJob
	wake      func() // probudí smyčku čtečky, když přijde požadavek na zápis

	// OnEvent se zavolá u každé zprávy a při změně počtu připojených webů
	// (používá ho ikonka v oznamovací oblasti). Nastavuje se před spuštěním.
	OnEvent func(m Message, clients int)
}

func NewHub() *Hub {
	return &Hub{
		clients:   map[*client]struct{}{},
		present:   map[string]Message{},
		readerMsg: Message{Type: "reader", Connected: ptr(false), Time: now()},
	}
}

func (h *Hub) notify(m Message) {
	if h.OnEvent != nil {
		h.OnEvent(m, h.Clients())
	}
}

func (h *Hub) add(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.notify(Message{Type: "clients"})
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	var dropped *writeJob
	if h.job != nil && h.job.client == c {
		dropped = h.job
		h.job = nil
	}
	h.mu.Unlock()
	if dropped != nil {
		dropped.timer.Stop()
		log.Printf("Zápis zrušen – web se odpojil")
		h.notify(Message{Type: "write-end"})
	}
	h.notify(Message{Type: "clients"})
}

func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) Broadcast(m Message) {
	if m.Time == "" {
		m.Time = now()
	}
	b, _ := json.Marshal(m)
	h.mu.Lock()
	switch m.Type {
	case "reader":
		h.readerMsg = m
		if m.Connected == nil || !*m.Connected {
			h.present = map[string]Message{}
		}
	case "card":
		h.present[m.Reader] = m
	case "removed":
		delete(h.present, m.Reader)
	}
	for c := range h.clients {
		select {
		case c.send <- b:
		default: // pomalý klient, zprávu zahodíme
		}
	}
	h.mu.Unlock()
	h.notify(m)
}

func (h *Hub) SetReader(name string, connected bool) {
	h.mu.Lock()
	same := h.readerMsg.Connected != nil && *h.readerMsg.Connected == connected && h.readerMsg.Reader == name
	h.mu.Unlock()
	if same {
		return
	}
	if connected {
		log.Printf("Čtečka připojena: %s", name)
	} else {
		log.Printf("Žádná čtečka není připojená – čekám…")
	}
	h.Broadcast(Message{Type: "reader", Reader: name, Connected: ptr(connected)})
}

func (h *Hub) currentReader() Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readerMsg
}

// presentCards vrátí karty, které právě leží na čtečkách (pro nově připojený web).
func (h *Hub) presentCards() []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Message
	for _, m := range h.present {
		m.Initial = true
		out = append(out, m)
	}
	return out
}

func (h *Hub) SetWaker(f func()) {
	h.mu.Lock()
	h.wake = f
	h.mu.Unlock()
}

// SubmitWrite zařadí požadavek na zápis. Starší čekající požadavek se zruší.
func (h *Hub) SubmitWrite(job *writeJob, timeout time.Duration) {
	h.mu.Lock()
	old := h.job
	h.job = job
	job.timer = time.AfterFunc(timeout, func() {
		h.mu.Lock()
		expired := h.job == job
		if expired {
			h.job = nil
		}
		h.mu.Unlock()
		if expired {
			log.Printf("Zápis vypršel – nikdo nepřiložil tag")
			job.client.Send(Message{Type: "write-result", ID: job.id, OK: ptr(false), Code: "timeout",
				Message: "Nikdo nepřiložil tag – zápis vypršel."})
			h.notify(Message{Type: "write-end"})
		}
	})
	wake := h.wake
	h.mu.Unlock()

	if old != nil {
		old.timer.Stop()
		old.client.Send(Message{Type: "write-result", ID: old.id, OK: ptr(false), Code: "replaced",
			Message: "Zápis byl nahrazen novějším požadavkem."})
	}
	log.Printf("Čekám na tag k zápisu (%d B)", len(WrapTLV(job.msg)))
	h.notify(Message{Type: "write-pending"})
	if wake != nil {
		wake()
	}
}

// CancelWrite zruší čekající zápis daného webu (id "" = jakýkoli jeho zápis).
func (h *Hub) CancelWrite(c *client, id string) bool {
	h.mu.Lock()
	job := h.job
	ok := job != nil && job.client == c && (id == "" || job.id == id)
	if ok {
		h.job = nil
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	job.timer.Stop()
	log.Printf("Zápis zrušen")
	c.Send(Message{Type: "write-result", ID: job.id, OK: ptr(false), Code: "cancelled", Message: "Zápis zrušen."})
	h.notify(Message{Type: "write-end"})
	return true
}

func (h *Hub) HasJob() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.job != nil
}

// TakeJob vyzvedne čekající zápis (jen jednou – druhý tag už se nezapíše).
func (h *Hub) TakeJob() *writeJob {
	h.mu.Lock()
	job := h.job
	h.job = nil
	h.mu.Unlock()
	if job != nil {
		job.timer.Stop()
	}
	return job
}

// FailJob ukončí čekající zápis chybou (např. když kartu nejde přečíst).
func (h *Hub) FailJob(code, message string) {
	job := h.TakeJob()
	if job == nil {
		return
	}
	log.Printf("Zápis selhal: %s", message)
	job.client.Send(Message{Type: "write-result", ID: job.id, OK: ptr(false), Code: code, Message: message})
	h.notify(Message{Type: "error"})
	h.notify(Message{Type: "write-end"})
}

// ---------- zpracování přiložené karty ----------

type cardInfo struct {
	uid, cardType, reader string
}

func isType2(cardType string) bool {
	// ACR122U hlásí NTAG i Ultralight stejně. U jiných čteček typ často neznáme,
	// tak to zkusíme – kontrola Capability Containeru špatný tag odmítne.
	return cardType == "MIFARE Ultralight / NTAG" || cardType == "MIFARE Ultralight C" || cardType == ""
}

// processCard se volá, když je karta na čtečce: nejdřív případný zápis,
// pak (u nově přiložené karty) přečtení obsahu a ohlášení webům.
func (h *Hub) processCard(t tagIO, ci cardInfo, arrival bool) {
	if job := h.TakeJob(); job != nil {
		h.doWrite(t, ci, job)
	}
	if !arrival {
		return
	}
	m := Message{Type: "card", UID: ci.uid, CardType: ci.cardType, Reader: ci.reader}
	if t != nil && isType2(ci.cardType) {
		info := readNDEF(t)
		if info.Formatted || ci.cardType != "" {
			m.NDEF = &info
		}
	}
	desc := ci.uid
	if ci.cardType != "" {
		desc += "  (" + ci.cardType + ")"
	}
	if m.NDEF != nil && len(m.NDEF.Records) > 0 {
		desc += "  " + summarizeRecords(m.NDEF.Records)
	}
	log.Printf("Karta přiložena: %s", desc)
	h.Broadcast(m)
}

func (h *Hub) doWrite(t tagIO, ci cardInfo, job *writeJob) {
	defer h.notify(Message{Type: "write-end"})
	fail := func(e *WriteError, res WriteResult) {
		log.Printf("Zápis na %s selhal: %s", ci.uid, e.Message)
		job.client.Send(Message{Type: "write-result", ID: job.id, OK: ptr(false), Code: e.Code, Message: e.Message,
			UID: ci.uid, CardType: ci.cardType, Bytes: res.Bytes, Capacity: res.Capacity})
		h.notify(Message{Type: "error"})
	}
	if t == nil || !isType2(ci.cardType) {
		fail(werr("unsupported", "Tenhle typ karty (%s) zatím zapisovat neumím – použij NTAG213/215/216.", ci.cardType), WriteResult{})
		return
	}
	res, e := writeNDEF(t, job.msg)
	if e != nil {
		fail(e, res)
		return
	}
	log.Printf("Zapsáno na %s: %s  (%d/%d B)", ci.uid, summarizeRecords(job.records), res.Bytes, res.Capacity)
	info := readNDEF(t) // aktuální obsah tagu, ať ho web může hned ukázat
	h.mu.Lock()
	if m, ok := h.present[ci.reader]; ok && m.UID == ci.uid {
		m.NDEF = &info
		h.present[ci.reader] = m
	}
	h.mu.Unlock()
	job.client.Send(Message{Type: "write-result", ID: job.id, OK: ptr(true), UID: ci.uid, CardType: ci.cardType,
		Bytes: res.Bytes, Capacity: res.Capacity, Records: job.records, NDEF: &info})
	h.notify(Message{Type: "written"})
}

func summarizeRecords(recs []Record) string {
	s := ""
	for i, r := range recs {
		if i > 0 {
			s += ", "
		}
		switch r.Type {
		case "url":
			s += r.URL
		case "text":
			t := []rune(r.Text)
			if len(t) > 30 {
				t = append(t[:30], '…')
			}
			s += "„" + string(t) + "“"
		case "mime":
			s += r.MimeType
		case "aar":
			s += "aplikace " + r.Package
		default:
			s += r.Type
		}
	}
	return "[" + s + "]"
}
