//go:build windows

// Windows: most běží jen jako ikonka v oznamovací oblasti (vedle hodin).
package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	//go:embed icons/gray.ico
	iconGray []byte
	//go:embed icons/green.ico
	iconGreen []byte
	//go:embed icons/purple.ico
	iconPurple []byte
	//go:embed icons/red.ico
	iconRed []byte
	//go:embed icons/orange.ico
	iconOrange []byte
)

const (
	appName   = "NFC most"
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	flashCard = 1500 * time.Millisecond
	flashErr  = 2500 * time.Millisecond
)

var logPath string

// ---------- stav ikonky ----------

type trayState struct {
	mu         sync.Mutex
	reader     string
	connected  bool
	clients    int
	lastUID    string
	lastType   string
	flashIcon  []byte
	flashUntil time.Time
	pending    bool // čeká se na tag k zápisu
	cfg        *Settings

	mStatus, mClients, mCopy *systray.MenuItem
	lastIcon                 []byte
}

func (s *trayState) handle(m Message, clients int) {
	s.mu.Lock()
	s.clients = clients
	switch m.Type {
	case "reader":
		s.connected = m.Connected != nil && *m.Connected
		s.reader = m.Reader
	case "card":
		s.lastUID, s.lastType = m.UID, m.CardType
		s.flashIcon, s.flashUntil = iconPurple, time.Now().Add(flashCard)
		time.AfterFunc(flashCard+50*time.Millisecond, s.refresh)
	case "written":
		s.flashIcon, s.flashUntil = iconPurple, time.Now().Add(flashCard)
		time.AfterFunc(flashCard+50*time.Millisecond, s.refresh)
	case "error":
		s.flashIcon, s.flashUntil = iconRed, time.Now().Add(flashErr)
		time.AfterFunc(flashErr+50*time.Millisecond, s.refresh)
	case "write-pending":
		s.pending = true
	case "write-end":
		s.pending = false
	}
	s.mu.Unlock()
	s.refresh()
}

func (s *trayState) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()

	icon := iconGray
	status := "čtečka nepřipojena"
	if s.connected {
		icon = iconGreen
		status = "čtečka připravena"
	}
	if s.pending {
		icon = iconOrange
		status = "čeká na tag k zápisu"
	}
	if time.Now().Before(s.flashUntil) {
		icon = s.flashIcon
	}
	if &icon[0] != lastIconPtr(s.lastIcon) {
		systray.SetIcon(icon)
		s.lastIcon = icon
	}

	tip := appName + " – " + status
	tip += fmt.Sprintf("\nPřipojené weby: %d", s.clients)
	if s.lastUID != "" {
		tip += "\nPoslední karta: " + s.lastUID
	}
	if s.cfg.Demo {
		tip += "\n(DEMO režim)"
	}
	systray.SetTooltip(tip)

	if s.connected {
		s.mStatus.SetTitle("Čtečka: " + s.reader)
	} else {
		s.mStatus.SetTitle("Čtečka nepřipojena")
	}
	s.mClients.SetTitle(fmt.Sprintf("Připojené weby: %d", s.clients))
	if s.lastUID != "" {
		title := "Kopírovat UID " + s.lastUID
		if s.lastType != "" {
			title += "  (" + s.lastType + ")"
		}
		s.mCopy.SetTitle(title)
		s.mCopy.Enable()
	}
}

func lastIconPtr(b []byte) *byte {
	if len(b) == 0 {
		return nil
	}
	return &b[0]
}

// ---------- spuštění ----------

func run(hub *Hub, cfg *Settings, lns []net.Listener) {
	setupLogFile(cfg)
	log.Printf("%s %s spuštěn na portu %d", appName, version, cfg.Port)

	st := &trayState{cfg: cfg}
	events := make(chan func(), 64)

	onReady := func() {
		systray.SetIcon(iconGray)
		systray.SetTitle(appName)
		systray.SetTooltip(appName)

		st.mStatus = systray.AddMenuItem("Hledám čtečku…", "")
		st.mStatus.Disable()
		st.mClients = systray.AddMenuItem("Připojené weby: 0", "")
		st.mClients.Disable()
		st.mCopy = systray.AddMenuItem("Kopírovat UID (zatím žádná karta)", "Zkopíruje UID poslední karty do schránky")
		st.mCopy.Disable()
		systray.AddSeparator()
		mOpen := systray.AddMenuItem("Otevřít testovací stránku a nastavení", "")
		mLog := systray.AddMenuItem("Otevřít záznam událostí", "")
		mFolder := systray.AddMenuItem("Otevřít složku s daty", "")
		mAuto := systray.AddMenuItemCheckbox("Spouštět po startu Windows", "", autostartEnabled())
		systray.AddSeparator()
		info := fmt.Sprintf("%s %s · port %d", appName, version, cfg.Port)
		if cfg.Demo {
			info += " · DEMO"
		}
		mInfo := systray.AddMenuItem(info, "")
		mInfo.Disable()
		mQuit := systray.AddMenuItem("Ukončit", "")

		testURL := fmt.Sprintf("http://localhost:%d", cfg.Port)

		// události z mostu zpracováváme postupně v jedné gorutině
		hub.OnEvent = func(m Message, clients int) {
			select {
			case events <- func() { st.handle(m, clients) }:
			default:
			}
		}
		go func() {
			for f := range events {
				f()
			}
		}()

		go func() {
			for {
				select {
				case <-st.mCopy.ClickedCh:
					st.mu.Lock()
					uid := st.lastUID
					st.mu.Unlock()
					if uid != "" {
						if err := copyToClipboard(uid); err != nil {
							log.Printf("Kopírování do schránky selhalo: %v", err)
						}
					}
				case <-mOpen.ClickedCh:
					openURL(testURL)
				case <-mLog.ClickedCh:
					openURL(logPath)
				case <-mFolder.ClickedCh:
					cfg.ensureDir()
					openFolder(cfg.Dir)
				case <-mAuto.ClickedCh:
					if mAuto.Checked() {
						if err := setAutostart(false, cfg); err != nil {
							showError("Automatické spouštění se nepodařilo vypnout:\n" + err.Error())
						} else {
							mAuto.Uncheck()
						}
					} else {
						if err := setAutostart(true, cfg); err != nil {
							showError("Automatické spouštění se nepodařilo zapnout:\n" + err.Error())
						} else {
							mAuto.Check()
						}
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()

		go serveAll(hub, cfg, lns)
		startReading(hub, cfg)
		st.refresh()
	}

	onExit := func() {
		log.Printf("%s ukončen", appName)
		for _, ln := range lns {
			ln.Close()
		}
	}

	systray.Run(onReady, onExit)
}

func alreadyRunning(cfg *Settings, port int) {
	setupLogFile(cfg)
	log.Printf("Most už běží na portu %d – druhá instance končí", port)
	showMessage(fmt.Sprintf("%s už běží – hledej ikonku vedle hodin (případně pod šipkou ^).", appName), mbWarning)
}

func noFreePort(cfg *Settings) {
	setupLogFile(cfg)
	log.Printf("Všechny porty mostu %v používají jiné programy", bridgePorts)
	showMessage(fmt.Sprintf("%s nemůže běžet: všechny jeho porty (%d, %d, %d) používají jiné programy.\n\n"+
		"Zkus zavřít programy, které by je mohly používat, nebo restartovat počítač.",
		appName, bridgePorts[0], bridgePorts[1], bridgePorts[2]), mbWarning)
}

// askPermission se zeptá uživatele, jestli smí web používat čtečku.
func askPermission(origin string) bool {
	text := fmt.Sprintf("Web\n\n    %s\n\nchce používat NFC čtečku: číst karty a zapisovat na tagy.\n\n"+
		"Povolit? Odpověď si %s zapamatuje, změnit ji jde na testovací stránce v Nastavení.",
		originLabel(origin), appName)
	return showMessage(text, mbQuestion) == idYes
}

func openFolder(path string) { openURL(path) }

func quitApp() { systray.Quit() }

// ---------- záznam událostí ----------

func setupLogFile(cfg *Settings) {
	logPath = cfg.LogPath()
	logs.Open(logPath, true, nil)
	log.SetOutput(logs)
	log.SetFlags(log.Ldate | log.Ltime)
}

// ---------- automatické spuštění ----------

func autostartCommand(cfg *Settings) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := `"` + exe + `"`
	for _, a := range cfg.Args {
		if strings.ContainsAny(a, " ,*\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		cmd += " " + a
	}
	return cmd, nil
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}

func setAutostart(on bool, cfg *Settings) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(appName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	cmd, err := autostartCommand(cfg)
	if err != nil {
		return err
	}
	log.Printf("Automatické spouštění zapnuto: %s", cmd)
	return k.SetStringValue(appName, cmd)
}

// ---------- drobnosti z Windows API ----------

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pMessageBox       = user32.NewProc("MessageBoxW")
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pSetClipboardData = user32.NewProc("SetClipboardData")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pMoveMemory       = kernel32.NewProc("RtlMoveMemory")
)

const (
	mbWarning  = 0x00000030 // MB_OK | MB_ICONWARNING
	mbQuestion = 0x00000024 // MB_YESNO | MB_ICONQUESTION
	idYes      = 6
)

func showMessage(text string, flags uintptr) uintptr {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(appName)
	const MB_SETFOREGROUND, MB_TOPMOST = 0x00010000, 0x00040000
	r, _, _ := pMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags|MB_SETFOREGROUND|MB_TOPMOST)
	return r
}

func showError(text string) { showMessage(text, mbWarning) }

func openURL(target string) {
	verb, _ := windows.UTF16PtrFromString("open")
	t, _ := windows.UTF16PtrFromString(target)
	if err := windows.ShellExecute(0, verb, t, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		log.Printf("Nelze otevřít %s: %v", target, err)
	}
}

func copyToClipboard(text string) error {
	const (
		CF_UNICODETEXT = 13
		GMEM_MOVEABLE  = 0x0002
	)
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	var opened uintptr
	for i := 0; i < 10; i++ { // schránku může chvíli držet jiný program
		if opened, _, _ = pOpenClipboard.Call(0); opened != 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if opened == 0 {
		return fmt.Errorf("schránka je obsazená")
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()

	size := uintptr(len(u) * 2)
	h, _, err := pGlobalAlloc.Call(GMEM_MOVEABLE, size)
	if h == 0 {
		return err
	}
	p, _, err := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return err
	}
	pMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	pGlobalUnlock.Call(h)
	if r, _, err := pSetClipboardData.Call(CF_UNICODETEXT, h); r == 0 {
		pGlobalFree.Call(h)
		return err
	}
	return nil
}
