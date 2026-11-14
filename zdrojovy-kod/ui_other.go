//go:build !windows

// Mimo Windows běží most jako obyčejný program v terminálu (hlavně pro vývoj).
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
)

func run(hub *Hub, s *Settings, lns []net.Listener) {
	logs.Open(s.LogPath(), false, os.Stdout)
	log.SetOutput(logs)
	log.SetFlags(log.Ltime)
	fmt.Println("===============================================")
	fmt.Printf("  NFC most %s\n", version)
	fmt.Printf("  WebSocket:         ws://127.0.0.1:%d/ws\n", s.Port)
	fmt.Printf("  Testovací stránka: http://localhost:%d\n", s.Port)
	fmt.Printf("  Data:              %s\n", s.Dir)
	fmt.Println("  Ukončení: Ctrl+C")
	fmt.Println("===============================================")
	startReading(hub, s)
	serveAll(hub, s, lns)
}

// Bez grafického rozhraní se nemáme koho zeptat – rozhoduje proměnná
// NFC_MOST_PERMISSION (allow / deny), výchozí je allow.
func askPermission(origin string) bool {
	return os.Getenv("NFC_MOST_PERMISSION") != "deny"
}

func openFolder(path string) { exec.Command("xdg-open", path).Start() }

func quitApp() { os.Exit(0) }

func alreadyRunning(s *Settings, port int) {
	log.Printf("NFC most už běží na portu %d.", port)
}

func noFreePort(s *Settings) {
	log.Printf("Všechny porty mostu (%v) používají jiné programy.", bridgePorts)
}
