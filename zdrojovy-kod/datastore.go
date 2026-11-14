// Data mostu ve složce %LOCALAPPDATA%\NFC most:
//
//	config.json       povolené / zamítnuté weby
//	nfc-most.log      záznam událostí (+ nfc-most.log.old)
//	presety\*.json    uložené presety zápisu
package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ---------- záznam událostí ----------

// logSink zapisuje log do souboru a umí ho za běhu smazat a založit znovu.
type logSink struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	crlf    bool      // Windows konce řádků
	also    io.Writer // kopie (např. terminál)
	stopped bool
}

var logs = &logSink{}

func (l *logSink) Open(path string, crlf bool, also io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path, l.crlf, l.also = path, crlf, also
	l.openLocked()
}

func (l *logSink) openLocked() {
	if l.path == "" || l.stopped {
		return
	}
	os.MkdirAll(filepath.Dir(l.path), 0o755)
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > 1<<20 { // ať log neroste donekonečna
		os.Rename(l.path, l.path+".old")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if fi, _ := f.Stat(); fi != nil && fi.Size() == 0 {
		f.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM, ať Poznámkový blok ukáže diakritiku
	}
	l.f = f
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.also != nil {
		l.also.Write(p)
	}
	if l.f != nil {
		out := p
		if l.crlf {
			out = []byte(strings.ReplaceAll(string(p), "\n", "\r\n"))
		}
		l.f.Write(out)
	}
	return len(p), nil
}

// Delete smaže soubory logu a (pokud reopen) začne nový.
func (l *logSink) Delete(reopen bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	var errs []error
	for _, p := range []string{l.path, l.path + ".old"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	if reopen {
		l.openLocked()
	} else {
		l.stopped = true
	}
	return errors.Join(errs...)
}

// ---------- presety ----------

// Preset – uložený formulář zápisu. Hesla se do presetů nikdy neukládají.
type Preset struct {
	Name  string            `json:"name"`
	Items []json.RawMessage `json:"items"` // položky formuláře testovací stránky
	Saved string            `json:"saved,omitempty"`
}

func presetFileName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case strings.ContainsRune(`<>:"/\|?*`, r), unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	n := strings.Trim(b.String(), ". ")
	if n == "" {
		n = "preset"
	}
	if r := []rune(n); len(r) > 60 {
		n = string(r[:60])
	}
	return n + ".json"
}

func (s *Settings) ListPresets() []Preset {
	out := []Preset{}
	files, _ := filepath.Glob(filepath.Join(s.PresetsDir(), "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p Preset
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		if p.Name == "" {
			p.Name = strings.TrimSuffix(filepath.Base(f), ".json")
		}
		if p.Items == nil {
			p.Items = []json.RawMessage{}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Settings) SavePreset(p Preset) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("chybí název presetu")
	}
	if len(p.Items) == 0 {
		return errors.New("preset je prázdný")
	}
	if err := s.presetsEnsure(); err != nil {
		return err
	}
	p.Saved = time.Now().Format(time.RFC3339)
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(filepath.Join(s.PresetsDir(), presetFileName(p.Name)), b, 0o644)
}

func (s *Settings) DeletePreset(name string) error {
	// najdeme soubor podle jména uvnitř (soubor mohl někdo přejmenovat ručně)
	files, _ := filepath.Glob(filepath.Join(s.PresetsDir(), "*.json"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var p Preset
		json.Unmarshal(b, &p)
		if p.Name == name || strings.TrimSuffix(filepath.Base(f), ".json") == name {
			return os.Remove(f)
		}
	}
	return fmt.Errorf("preset %q nenalezen", name)
}

// ---------- export a mazání ----------

// Export zabalí složku s daty do zipu.
func (s *Settings) Export(w io.Writer) error {
	zw := zip.NewWriter(w)
	err := filepath.Walk(s.Dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.Dir, path)
		h, _ := zip.FileInfoHeader(fi)
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		zf, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // např. zamčený soubor přeskočíme
		}
		defer f.Close()
		_, err = io.Copy(zf, f)
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

func (s *Settings) DeletePresets() error { return os.RemoveAll(s.PresetsDir()) }

// DeleteAll smaže celou složku s daty (most se pak ukončí).
func (s *Settings) DeleteAll() error {
	logs.Delete(false)
	s.mu.Lock()
	s.allowed, s.denied = nil, nil
	s.mu.Unlock()
	return os.RemoveAll(s.Dir)
}
