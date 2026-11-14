// HTTP a WebSocket server mostu.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

//go:embed icons/green.ico
var faviconICO []byte

var features = append([]string{"read", "ndef-read", "write", "permission"}, writableTypes...)

// maximální velikost dat, která se vejde na největší běžný tag (NTAG216)
const maxTagBytes = 872

// ---------- weby ----------

func isLocalHost(hostname string) bool {
	h := strings.ToLower(strings.Trim(hostname, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// isLocalOrigin – požadavek z testovací stránky mostu.
func isLocalOrigin(origin string, port int) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return isLocalHost(u.Hostname()) && u.Port() == strconv.Itoa(port)
}

// normalizePattern upraví web: "https://Muj-Web.cz/stranka/" → "https://muj-web.cz"
func normalizePattern(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if i := strings.Index(p, "://"); i >= 0 {
		rest := p[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			rest = rest[:j]
		}
		return p[:i+3] + rest
	}
	return strings.TrimRight(p, "/")
}

// originMatches – přesná shoda ("https://muj-web.cz"), doména bez schématu
// ("muj-web.cz") nebo maska ("*.muj-web.cz").
func originMatches(origin string, patterns []string) bool {
	o := normalizePattern(origin)
	u, err := url.Parse(origin)
	host, hostname := "", ""
	if err == nil {
		host, hostname = strings.ToLower(u.Host), strings.ToLower(u.Hostname())
	}
	for _, p := range patterns {
		p = normalizePattern(p)
		if p == "" {
			continue
		}
		if p == o {
			return true
		}
		if strings.Contains(p, "://") {
			if m, _ := path.Match(p, o); m {
				return true
			}
			continue
		}
		if host == "" {
			continue
		}
		if m, _ := path.Match(p, host); m {
			return true
		}
		if m, _ := path.Match(p, hostname); m {
			return true
		}
	}
	return false
}

func originLabel(origin string) string {
	if origin == "null" {
		return "stránka otevřená ze souboru (file://)"
	}
	return origin
}

// ---------- WebSocket ----------

type inMessage struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Records []Record `json:"records"`
	URL     string   `json:"url"`
	Text    string   `json:"text"`
	Lang    string   `json:"lang"`
	Timeout float64  `json:"timeout"` // sekundy
}

var jobCounter atomic.Int64

func (in inMessage) records() []Record {
	if len(in.Records) > 0 {
		return in.Records
	}
	var r []Record
	if in.URL != "" {
		r = append(r, Record{Type: "url", URL: in.URL})
	}
	if in.Text != "" {
		r = append(r, Record{Type: "text", Text: in.Text, Lang: in.Lang})
	}
	return r
}

func handleIncoming(hub *Hub, c *client, in inMessage) {
	switch in.Type {
	case "write":
		if in.ID == "" {
			in.ID = fmt.Sprintf("w%d", jobCounter.Add(1))
		}
		if !c.canWrite {
			c.Send(Message{Type: "write-result", ID: in.ID, OK: ptr(false), Code: "forbidden",
				Message: "Tento web nemá povolený přístup k NFC mostu."})
			return
		}
		recs := in.records()
		msg, err := EncodeNDEF(recs)
		if err != nil {
			c.Send(Message{Type: "write-result", ID: in.ID, OK: ptr(false), Code: "invalid", Message: err.Error()})
			return
		}
		size := len(WrapTLV(msg))
		if size > maxTagBytes {
			c.Send(Message{Type: "write-result", ID: in.ID, OK: ptr(false), Code: "too-large", Bytes: size,
				Message: fmt.Sprintf("Data mají %d B – to se nevejde na žádný běžný tag.", size)})
			return
		}
		timeout := time.Duration(in.Timeout * float64(time.Second))
		if in.Timeout <= 0 {
			timeout = 30 * time.Second
		}
		timeout = min(max(timeout, time.Second), 5*time.Minute)
		c.Send(Message{Type: "write-pending", ID: in.ID, Bytes: size, Message: "Přilož tag ke čtečce…"})
		hub.SubmitWrite(&writeJob{id: in.ID, client: c, records: recs, msg: msg}, timeout)

	case "cancel":
		hub.CancelWrite(c, in.ID)

	case "measure":
		msg, err := EncodeNDEF(in.records())
		if err != nil {
			c.Send(Message{Type: "measure-result", ID: in.ID, OK: ptr(false), Code: "invalid", Message: err.Error()})
			return
		}
		c.Send(Message{Type: "measure-result", ID: in.ID, OK: ptr(true), Bytes: len(WrapTLV(msg))})

	default:
		c.Send(Message{Type: "error", ID: in.ID, Code: "unknown-command",
			Message: fmt.Sprintf("Neznámý příkaz %q (umím: write, cancel, measure).", in.Type)})
	}
}

func wsHandler(hub *Hub, s *Settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(64 << 10)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		write := func(m Message) error {
			if m.Time == "" {
				m.Time = now()
			}
			b, _ := json.Marshal(m)
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			defer wcancel()
			return c.Write(wctx, websocket.MessageText, b)
		}
		hello := func(perm string) error {
			return write(Message{Type: "hello", App: "nfc-most", Version: version, Permission: perm,
				CanWrite: ptr(perm == "granted"), Features: features})
		}

		// Jediná čtecí gorutina pro celé spojení – zprávy jdou do inboxu.
		inbox := make(chan inMessage, 16)
		go func() {
			defer cancel()
			for {
				typ, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				if typ != websocket.MessageText {
					continue
				}
				var in inMessage
				if err := json.Unmarshal(data, &in); err != nil {
					in = inMessage{Type: "bad-json"}
				}
				select {
				case inbox <- in:
				case <-ctx.Done():
					return
				}
			}
		}()

		// ---- povolení webu ----
		switch s.Status(origin) {
		case "denied":
			hello("denied")
			c.Close(4003, "zamítnuto")
			return
		case "unknown":
			if hello("pending") != nil {
				return
			}
			ask := s.Ask(origin)
			select {
			case <-ask.done:
			case <-ctx.Done():
				return
			}
			if !ask.allowed {
				write(Message{Type: "permission", Permission: "denied", Message: "Uživatel přístup zamítl."})
				c.Close(4003, "zamítnuto")
				return
			}
			if write(Message{Type: "permission", Permission: "granted", CanWrite: ptr(true)}) != nil {
				return
			}
		default: // local, granted
			if hello("granted") != nil {
				return
			}
		}
		serveClient(hub, c, ctx, origin, inbox)
	}
}

// serveClient obsluhuje web s povoleným přístupem.
func serveClient(hub *Hub, c *websocket.Conn, ctx context.Context, origin string, inbox <-chan inMessage) {
	name := origin
	if name == "" {
		name = "program bez Origin"
	}
	log.Printf("Připojil se web: %s", originLabel(name))
	defer log.Printf("Odpojil se web: %s", originLabel(name))

	cl := &client{send: make(chan []byte, 64), origin: origin, canWrite: true}
	cl.Send(hub.currentReader())
	for _, m := range hub.presentCards() {
		cl.Send(m)
	}
	hub.add(cl)
	defer hub.remove(cl)

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-inbox:
			if in.Type == "bad-json" {
				cl.Send(Message{Type: "error", Code: "bad-json", Message: "Zpráva není platný JSON."})
				continue
			}
			handleIncoming(hub, cl, in)
		case b := <-cl.send:
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// ---------- HTTP ----------

// Ochrana proti DNS rebinding: most odpovídá jen na adresy localhost.
func hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if !isLocalHost(host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}

type configDTO struct {
	App     string   `json:"app"`
	Version string   `json:"version"`
	Port    int      `json:"port"`
	Ports   []int    `json:"ports"`
	Allowed []string `json:"allowed"`
	Denied  []string `json:"denied"`
	Dir     string   `json:"dir"`
	Demo    bool     `json:"demo"`
}

func newMux(hub *Hub, s *Settings) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler(hub, s))

	// Akce, které něco mění, smí volat jen testovací stránka mostu.
	local := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !isLocalOrigin(r.Header.Get("Origin"), s.Port) {
				jsonErr(w, http.StatusForbidden, "Tohle jde jen z testovací stránky NFC mostu.")
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		m := hub.currentReader()
		m.App, m.Version = "nfc-most", version
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, 200, m)
	})

	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		allowed, denied := s.Lists()
		writeJSON(w, 200, configDTO{"nfc-most", version, s.Port, bridgePorts, allowed, denied, s.Dir, s.Demo})
	})

	mux.HandleFunc("POST /sites/forget", local(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Origin string `json:"origin"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body) != nil || body.Origin == "" {
			jsonErr(w, 400, "Chybí web.")
			return
		}
		if err := s.Forget(body.Origin); err != nil {
			jsonErr(w, 500, "Nastavení nejde uložit: "+err.Error())
			return
		}
		log.Printf("Web odebrán ze seznamu: %s", originLabel(body.Origin))
		allowed, denied := s.Lists()
		writeJSON(w, 200, map[string]any{"allowed": allowed, "denied": denied})
	}))

	// ---- presety ----
	mux.HandleFunc("GET /presets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.ListPresets())
	})
	mux.HandleFunc("POST /presets", local(func(w http.ResponseWriter, r *http.Request) {
		var p Preset
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
			jsonErr(w, 400, "Neplatná data presetu.")
			return
		}
		if err := s.SavePreset(p); err != nil {
			jsonErr(w, 400, err.Error())
			return
		}
		log.Printf("Preset uložen: %s", p.Name)
		writeJSON(w, 200, s.ListPresets())
	}))
	mux.HandleFunc("DELETE /presets", local(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if err := s.DeletePreset(name); err != nil {
			jsonErr(w, 404, err.Error())
			return
		}
		log.Printf("Preset smazán: %s", name)
		writeJSON(w, 200, s.ListPresets())
	}))

	// ---- data ----
	mux.HandleFunc("GET /export.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nfc-most-data-%s.zip"`, time.Now().Format("2006-01-02")))
		if err := s.Export(w); err != nil {
			log.Printf("Export selhal: %v", err)
		}
	})
	mux.HandleFunc("POST /data/open-folder", local(func(w http.ResponseWriter, r *http.Request) {
		s.ensureDir()
		openFolder(s.Dir)
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /data/delete", local(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			What string `json:"what"` // presets | logs | all
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
		var err error
		switch body.What {
		case "presets":
			err = s.DeletePresets()
			log.Printf("Presety smazány")
		case "logs":
			err = logs.Delete(true)
			log.Printf("Záznam událostí smazán")
		case "all":
			log.Printf("Mažu všechna data a ukončuji most")
			err = s.DeleteAll()
			writeJSON(w, 200, map[string]any{"ok": err == nil, "quitting": true})
			go func() { time.Sleep(500 * time.Millisecond); quitApp() }()
			return
		default:
			jsonErr(w, 400, "Neznámá akce.")
			return
		}
		if err != nil {
			jsonErr(w, 500, "Smazání se nepovedlo: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(faviconICO)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(testPage)
	})
	return hostGuard(mux)
}
