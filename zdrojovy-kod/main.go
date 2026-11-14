// NFC most – čte a zapisuje NFC tagy přes čtečku (PC/SC, např. ACR122U)
// a zpřístupňuje je webovým aplikacím přes WebSocket.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "1.3.0"

// Porty, na kterých most může běžet. Použije první volný v tomto pořadí;
// webové aplikace zkoušejí všechny tři.
var bridgePorts = []int{7878, 3535, 6262}

//go:embed index.html
var testPage []byte

// ---------- nastavení a povolené weby ----------

type Settings struct {
	mu      sync.Mutex
	allowed []string // weby, kterým uživatel povolil přístup
	denied  []string // weby, které uživatel zamítl
	asking  map[string]*permissionAsk

	Port int  // port, na kterém most skutečně běží
	Demo bool // demo režim bez čtečky
	Args []string
	Dir  string // složka s daty (%LOCALAPPDATA%\NFC most)
}

type permissionAsk struct {
	done    chan struct{}
	allowed bool
}

type fileConfig struct {
	Allowed []string `json:"allowed"`
	Denied  []string `json:"denied"`
	Origins []string `json:"origins,omitempty"` // verze 1.2 – převede se do allowed
}

func appDataDir() string {
	dir, err := os.UserCacheDir() // Windows: %LOCALAPPDATA%
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "NFC most")
}

func (s *Settings) ConfigPath() string   { return filepath.Join(s.Dir, "config.json") }
func (s *Settings) PresetsDir() string   { return filepath.Join(s.Dir, "presety") }
func (s *Settings) LogPath() string      { return filepath.Join(s.Dir, "nfc-most.log") }
func (s *Settings) ensureDir() error     { return os.MkdirAll(s.Dir, 0o755) }
func (s *Settings) presetsEnsure() error { return os.MkdirAll(s.PresetsDir(), 0o755) }

func loadSettings() *Settings {
	demo := flag.Bool("demo", false, "bez čtečky: střídavě přikládá virtuální tagy (pro testování webu)")
	flag.Parse()

	s := &Settings{Demo: *demo, Args: os.Args[1:], Dir: appDataDir(), asking: map[string]*permissionAsk{}}
	s.ensureDir()

	var fc fileConfig
	if b, err := os.ReadFile(s.ConfigPath()); err == nil {
		if err := json.Unmarshal(b, &fc); err != nil {
			log.Printf("config.json je poškozený, používám výchozí nastavení: %v", err)
		}
	}
	s.allowed = cleanOrigins(append(fc.Allowed, fc.Origins...))
	s.denied = cleanOrigins(fc.Denied)
	if len(fc.Origins) > 0 {
		s.save() // převod ze staré verze
	}
	return s
}

func cleanOrigins(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, o := range list {
		p := normalizePattern(o)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (s *Settings) save() error {
	s.mu.Lock()
	fc := fileConfig{Allowed: append([]string{}, s.allowed...), Denied: append([]string{}, s.denied...)}
	s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(fc, "", "  ")
	return os.WriteFile(s.ConfigPath(), b, 0o644)
}

func (s *Settings) Lists() (allowed, denied []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.allowed...), append([]string{}, s.denied...)
}

// Status vrátí stav webu: "local" (testovací stránka nebo program bez Origin),
// "granted", "denied" nebo "unknown" (ještě se nikdo neptal).
func (s *Settings) Status(origin string) string {
	if origin == "" || isLocalOrigin(origin, s.Port) {
		return "local"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if originMatches(origin, s.denied) {
		return "denied"
	}
	if originMatches(origin, s.allowed) {
		return "granted"
	}
	return "unknown"
}

// Ask zobrazí uživateli dotaz na povolení (jen jednou pro každý web,
// další připojení čekají na stejnou odpověď).
func (s *Settings) Ask(origin string) *permissionAsk {
	key := normalizePattern(origin)
	s.mu.Lock()
	if a, ok := s.asking[key]; ok {
		s.mu.Unlock()
		return a
	}
	a := &permissionAsk{done: make(chan struct{})}
	s.asking[key] = a
	s.mu.Unlock()

	go func() {
		log.Printf("Ptám se na povolení pro %s", origin)
		allowed := askPermission(origin)
		s.Decide(origin, allowed)
		if allowed {
			log.Printf("Povoleno: %s", origin)
		} else {
			log.Printf("Zamítnuto: %s", origin)
		}
		s.mu.Lock()
		a.allowed = allowed
		delete(s.asking, key)
		s.mu.Unlock()
		close(a.done)
	}()
	return a
}

func remove(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func (s *Settings) Decide(origin string, allow bool) error {
	o := normalizePattern(origin)
	s.mu.Lock()
	s.allowed, s.denied = remove(s.allowed, o), remove(s.denied, o)
	if allow {
		s.allowed = append(s.allowed, o)
	} else {
		s.denied = append(s.denied, o)
	}
	s.mu.Unlock()
	return s.save()
}

// Forget odebere web z obou seznamů – příště se most zeptá znovu.
func (s *Settings) Forget(origin string) error {
	o := normalizePattern(origin)
	s.mu.Lock()
	s.allowed, s.denied = remove(s.allowed, o), remove(s.denied, o)
	s.mu.Unlock()
	return s.save()
}

// ---------- spuštění ----------

func startReading(hub *Hub, s *Settings) {
	if s.Demo {
		log.Printf("DEMO režim – čtečka se nepoužívá")
		go demoLoop(hub)
	} else {
		log.Printf("Hledám čtečku…")
		go readerLoop(hub)
	}
}

func serveAll(hub *Hub, s *Settings, lns []net.Listener) {
	h := newMux(hub, s)
	for _, ln := range lns[1:] {
		go http.Serve(ln, h)
	}
	if err := http.Serve(lns[0], h); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("Server skončil s chybou: %v", err)
	}
}

// isOurBridge zjistí, jestli na portu už běží NFC most (a ne jiný program).
func isOurBridge(port int) bool {
	cl := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var m struct {
		App string `json:"app"`
	}
	json.NewDecoder(resp.Body).Decode(&m)
	return m.App == "nfc-most"
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "10048") ||
		strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

func main() {
	s := loadSettings()
	hub := NewHub()

	var ln4 net.Listener
	for _, port := range bridgePorts {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln4, s.Port = ln, port
			break
		}
		if isOurBridge(port) {
			alreadyRunning(s, port)
			return
		}
		log.Printf("Port %d používá jiný program, zkouším další", port)
	}
	if ln4 == nil {
		noFreePort(s)
		return
	}
	lns := []net.Listener{ln4}

	// Prohlížeč může "localhost" přeložit i na IPv6 adresu ::1. Posloucháme i tam,
	// aby se web omylem nepřipojil k jinému programu, který by na ::1 seděl.
	if ln6, err := net.Listen("tcp", fmt.Sprintf("[::1]:%d", s.Port)); err == nil {
		lns = append(lns, ln6)
	} else if isAddrInUse(err) {
		log.Printf("POZOR: port %d na IPv6 (::1) používá jiný program.", s.Port)
	}

	run(hub, s, lns) // platformně závislé: ikonka (Windows) nebo konzole
}
