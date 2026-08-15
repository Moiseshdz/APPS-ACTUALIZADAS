package main

import (
	"archive/zip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	appFamily      = "sistema-notas-local"
	appVersion     = "4.6.0-buscador-resultados-sin-select"
	appID          = appFamily + "-" + appVersion
	bindHost       = "0.0.0.0"
	loopbackHost   = "127.0.0.1"
	firstPort      = 8765
	lastPort       = 8795
	maxBodyBytes   = 30 << 20
	maxPhotos      = 30
	statusNew      = "NUEVO"
	statusOpen     = "ABIERTO"
	statusUsed     = "USADO"
	statusClosed   = "INCIDENTE CERRADO"
	autoCloseAfter = 30 * time.Minute
)

//go:embed web/* web/assets/* closure_codes.json corrections.json orthography_vocab.json
var embeddedFiles embed.FS

type Note struct {
	ID                   int64  `json:"id"`
	Folio                string `json:"folio"`
	FolioManual          string `json:"folioManual"`
	FechaClave           string `json:"fechaClave"`
	Titulo               string `json:"titulo"`
	Corporacion          string `json:"corporacion"`
	Municipio            string `json:"municipio"`
	Operador             string `json:"operador"`
	ContenidoHTML        string `json:"contenidoHtml"`
	WorkflowStatus       string `json:"workflowStatus,omitempty"`
	OpenedAt             string `json:"openedAt,omitempty"`
	UsedAt               string `json:"usedAt,omitempty"`
	ClosedAt             string `json:"closedAt,omitempty"`
	ClosureCode          string `json:"closureCode,omitempty"`
	ClosureName          string `json:"closureName,omitempty"`
	ClosureMethod        string `json:"closureMethod,omitempty"`
	ClosureReason        string `json:"closureReason,omitempty"`
	AutoCloseEligible    bool   `json:"autoCloseEligible,omitempty"`
	OrthographyCorrected bool   `json:"orthographyCorrected,omitempty"`
	PhotoOnly            bool   `json:"photoOnly,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type ClosureCode struct {
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	Definition       string   `json:"definition"`
	SourceName       string   `json:"sourceName,omitempty"`
	SourceDefinition string   `json:"sourceDefinition,omitempty"`
	SourcePage       int      `json:"sourcePage,omitempty"`
	Strong           []string `json:"strong"`
	Concepts         []string `json:"concepts"`
}

type IncidentTipification struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Priority string `json:"priority"`
}

type IncidentCatalogFile struct {
	Source string                 `json:"source"`
	Count  int                    `json:"count"`
	Items  []IncidentTipification `json:"items"`
}

type ClosureCandidate struct {
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	Definition string   `json:"definition"`
	Score      float64  `json:"score"`
	Evidence   []string `json:"evidence,omitempty"`
}

type ClosureAnalysis struct {
	Recommended     *ClosureCandidate  `json:"recommended,omitempty"`
	Alternatives    []ClosureCandidate `json:"alternatives"`
	Confidence      string             `json:"confidence"`
	Reason          string             `json:"reason"`
	Profile         string             `json:"profile,omitempty"`
	ProfileLabel    string             `json:"profileLabel,omitempty"`
	SafeToAutoClose bool               `json:"safeToAutoClose"`
}

type CorrectionRule struct {
	Key         string
	Replacement string
	Pattern     *regexp.Regexp
}

type Photo struct {
	ID         int64  `json:"id"`
	NoteID     int64  `json:"noteId"`
	StoredName string `json:"storedName"`
	Name       string `json:"name"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	CreatedAt  string `json:"createdAt"`
}

type Audit struct {
	ID        int64          `json:"id"`
	Action    string         `json:"action"`
	NoteID    int64          `json:"noteId,omitempty"`
	Folio     string         `json:"folio,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type Database struct {
	Version     int64   `json:"version"`
	NextNoteID  int64   `json:"nextNoteId"`
	NextPhotoID int64   `json:"nextPhotoId"`
	NextAuditID int64   `json:"nextAuditId"`
	Notes       []Note  `json:"notes"`
	Photos      []Photo `json:"photos"`
	Audit       []Audit `json:"audit"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	uploads string
	db      Database
}

type noteSummary struct {
	Note
	PhotoCount   int   `json:"photoCount"`
	CoverPhotoID int64 `json:"coverPhotoId,omitempty"`
}

type fullNote struct {
	Note
	PhotoCount   int           `json:"photoCount"`
	CoverPhotoID int64         `json:"coverPhotoId,omitempty"`
	Photos       []photoPublic `json:"photos"`
}

type photoPublic struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url"`
}

var allowedTag = regexp.MustCompile(`(?is)<\s*(/?)\s*([a-zA-Z0-9]+)(?:\s[^>]*)?>`)
var dangerousBlock = regexp.MustCompile(`(?is)<(?:script|style|iframe|object|embed)[^>]*>.*?</(?:script|style|iframe|object|embed)\s*>`)
var stripTags = regexp.MustCompile(`(?is)<[^>]+>`)
var nonDigits = regexp.MustCompile(`\D`)
var wordPattern = regexp.MustCompile(`[\p{L}ÁÉÍÓÚÜÑáéíóúüñ]{3,}`)
var htmlLineBreaks = regexp.MustCompile(`(?is)<\s*br\s*/?\s*>|</\s*(?:p|div|li|h[1-6])\s*>`)

var (
	serverPort           int
	serverURLs           []string
	closureCatalog       []ClosureCode
	closureByCode        map[string]ClosureCode
	orthographyRules     []CorrectionRule
	orthographyLexicon   map[string][]string
	orthographyByInitial map[rune][]string
	orthographyByLength  map[int][]string
	orthographyFrequency map[string]int
	incidentCatalog      []IncidentTipification
	serverStartedAt      string
	serverHostName       string
	serverOSUser         string
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		switch strings.ToLower(strings.TrimSpace(os.Args[1])) {
		case "--servidor", "servidor", "server":
			mode = "server"
		case "--cliente", "cliente", "client":
			mode = "client"
		case "--configurar-cliente", "--config", "config":
			mode = "client-config"
		}
	}

	if mode == "" {
		selected, err := runRoleChooser()
		if err != nil {
			showFatal("No se pudo abrir el selector de modo: " + err.Error())
			return
		}
		mode = selected
	}

	switch mode {
	case "server":
		runServerMode()
	case "client":
		runClientMode(false)
	case "client-config":
		runClientMode(true)
	default:
		showFatal("Modo de ejecución no válido.")
	}
}

func runRoleChooser() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	choice := make(chan string, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 8 * time.Second}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, `<!doctype html><html lang="es-MX"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sistema de Notas</title><style>
		*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#dedede;font-family:Segoe UI,Arial,sans-serif;color:#173d59}.card{width:min(850px,calc(100% - 28px));background:#fff;border:2px solid #0d3553;border-radius:20px;padding:28px;box-shadow:0 16px 38px rgba(0,0,0,.17)}h1{margin:0 0 8px;font-size:28px}.sub{margin:0 0 22px;color:#586b79;line-height:1.5}.grid{display:grid;grid-template-columns:1fr 1fr;gap:18px}.option{border:2px solid #0d3553;border-radius:17px;background:#2a66a1;padding:22px;color:#fff;display:flex;flex-direction:column;min-height:245px}.option h2{margin:0 0 10px;color:#ffcc00}.option p{margin:0;line-height:1.5;flex:1}.option button{width:100%;min-height:54px;margin-top:22px;border:2px solid #0d3553;border-radius:11px;background:#154e73;color:#ffcc00;font-size:16px;font-weight:800;cursor:pointer}.option.secondary{background:#eef4f8;color:#173d59}.option.secondary h2{color:#154e73}.small{margin-top:18px;color:#667985;font-size:13px;line-height:1.5}@media(max-width:720px){.grid{grid-template-columns:1fr}.card{padding:20px}h1{font-size:23px}}
		</style></head><body><main class="card"><h1>Sistema de Notas Informativas</h1><p class="sub">Este mismo ejecutable sirve en las dos computadoras. En cada PC selecciona cómo se utilizará.</p><div class="grid"><form class="option" method="post" action="/select"><h2>Computadora principal</h2><p>Inicia el servidor central, guarda la base de datos y las fotografías. Esta computadora debe permanecer encendida mientras se use el sistema.</p><input type="hidden" name="mode" value="server"><button type="submit">INICIAR COMO SERVIDOR</button></form><form class="option secondary" method="post" action="/select"><h2>Segunda computadora</h2><p>Se conecta a la computadora principal. Los registros, cambios, eliminaciones y fotografías se reflejan en ambas pantallas.</p><input type="hidden" name="mode" value="client"><button type="submit">CONECTAR COMO CLIENTE</button></form></div><p class="small"><b>Importante:</b> usa “Servidor” únicamente en la PC que conservará la carpeta <code>datos</code>. En la otra PC usa “Cliente”.</p></main></body></html>`)
	})

	mux.HandleFunc("/select", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "No se pudo leer la selección", http.StatusBadRequest)
			return
		}
		mode := strings.TrimSpace(r.FormValue("mode"))
		if mode != "server" && mode != "client" {
			http.Error(w, "Selección no válida", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html lang="es-MX"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Iniciando</title><style>body{font-family:Segoe UI,Arial,sans-serif;background:#dedede;margin:0;min-height:100vh;display:grid;place-items:center;color:#154e73}.box{background:#fff;border:2px solid #0d3553;border-radius:18px;padding:30px;text-align:center;box-shadow:0 14px 32px rgba(0,0,0,.15)}.dot{width:16px;height:16px;margin:18px auto;border-radius:50%;background:#ffcc00;animation:p 1s infinite alternate}@keyframes p{to{transform:scale(1.7);opacity:.45}}</style></head><body><div class="box"><h2>Iniciando el sistema…</h2><div class="dot"></div><p>Esta ventana puede cerrarse cuando se abra la aplicación.</p></div></body></html>`)
		select {
		case choice <- mode:
		default:
		}
	})

	go func() {
		_ = server.Serve(listener)
	}()
	openBrowser(fmt.Sprintf("http://127.0.0.1:%d/", port))
	selected := <-choice
	_ = server.Close()
	return selected, nil
}

func runClientMode(forceConfig bool) {
	root, err := applicationDir()
	if err != nil {
		showFatal(err.Error())
		return
	}
	configPath := filepath.Join(root, "servidor.txt")
	current := readServerConfig(configPath)
	if !forceConfig && current != "" {
		if _, err := checkRemoteServer(current); err == nil {
			openBrowser(strings.TrimRight(current, "/") + "/?vista=dashboard")
			return
		}
	}
	if err := runClientConfigurator(configPath, current); err != nil {
		showFatal(err.Error())
	}
}

func normalizeServerAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("Escribe el enlace mostrado en la computadora principal")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("El enlace no es válido")
	}
	if parsed.Port() == "" {
		return "", fmt.Errorf("El enlace debe incluir el puerto, por ejemplo: http://192.168.1.25:8765/")
	}
	parsed.Path = "/"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/") + "/", nil
}

func checkRemoteServer(base string) (map[string]any, error) {
	status := map[string]any{}
	normalized, err := normalizeServerAddress(base)
	if err != nil {
		return status, err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(normalized + "api/status")
	if err != nil {
		return status, fmt.Errorf("no se pudo conectar con la computadora principal")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("el servidor respondió con error %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err != nil {
		return status, fmt.Errorf("la respuesta del servidor no es válida")
	}
	if ok, _ := status["ok"].(bool); !ok {
		return status, fmt.Errorf("el servidor no está disponible")
	}
	if family, _ := status["family"].(string); family != appFamily {
		return status, fmt.Errorf("el enlace no corresponde al Sistema de Notas")
	}
	return status, nil
}

func readServerConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	value, err := normalizeServerAddress(string(data))
	if err != nil {
		return ""
	}
	return value
}

func runClientConfigurator(configPath, current string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("no se pudo abrir el configurador: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 8 * time.Second}

	render := func(w http.ResponseWriter, value, errorText string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="es-MX"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Conectar Sistema de Notas</title><style>
		body{font-family:Segoe UI,Arial,sans-serif;background:#e2e2e2;margin:0;min-height:100vh;display:grid;place-items:center;color:#173d59}.card{width:min(650px,calc(100%% - 30px));background:white;border:2px solid #0d3553;border-radius:18px;padding:26px;box-shadow:0 14px 34px rgba(0,0,0,.16)}h1{margin:0 0 10px}.hint{color:#526573;line-height:1.5}.error{background:#fff0f1;color:#9a2535;border:1px solid #cf7b86;border-radius:10px;padding:10px;margin:14px 0}label{display:block;font-weight:700;margin:18px 0 7px}input{width:100%%;box-sizing:border-box;height:48px;border:2px solid #0d3553;border-radius:10px;padding:0 13px;font-size:16px}button{width:100%%;margin-top:16px;height:50px;border:2px solid #0d3553;border-radius:11px;background:#154e73;color:#ffcc00;font-weight:800;font-size:16px;cursor:pointer}.small{font-size:13px;color:#687985;margin-top:16px}</style></head><body><form class="card" method="post" action="/save"><h1>Conectar la segunda computadora</h1><p class="hint">En la computadora principal abre este mismo ejecutable, selecciona <b>Servidor</b>, pulsa <b>Copiar enlace PC 2</b> y pega aquí el enlace.</p>%s<label>Enlace del servidor central</label><input name="server" value="%s" placeholder="http://192.168.1.25:8765/" autofocus required><button type="submit">GUARDAR Y ABRIR EL SISTEMA</button><p class="small">Ambas computadoras deben estar conectadas a la misma red. La computadora principal debe permanecer encendida.</p></form></body></html>`, clientErrorBox(errorText), html.EscapeString(value))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		render(w, current, "")
	})
	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			render(w, current, "No se pudo leer el enlace.")
			return
		}
		value, err := normalizeServerAddress(r.FormValue("server"))
		if err != nil {
			render(w, r.FormValue("server"), err.Error())
			return
		}
		if _, err := checkRemoteServer(value); err != nil {
			render(w, value, "No se pudo conectar: "+err.Error()+". Verifica que el servidor esté abierto y que el Firewall permita la conexión.")
			return
		}
		if err := os.WriteFile(configPath, []byte(value), 0644); err != nil {
			render(w, value, "No se pudo guardar la configuración: "+err.Error())
			return
		}
		http.Redirect(w, r, value+"?vista=dashboard", http.StatusSeeOther)
		go func() {
			time.Sleep(2 * time.Second)
			_ = server.Close()
		}()
	})

	openBrowser(fmt.Sprintf("http://127.0.0.1:%d/", port))
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func clientErrorBox(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	return `<div class="error">` + html.EscapeString(message) + `</div>`
}

func runServerMode() {
	root, err := applicationDir()
	if err != nil {
		showFatal(err.Error())
		return
	}
	dataDir := filepath.Join(root, "datos")
	uploads := filepath.Join(dataDir, "uploads")
	backups := filepath.Join(root, "respaldos")
	_ = os.MkdirAll(uploads, 0755)
	_ = os.MkdirAll(backups, 0755)

	logFile, _ := os.OpenFile(filepath.Join(dataDir, "sistema_notas.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if logFile != nil {
		defer logFile.Close()
		log.SetOutput(logFile)
	}

	// Antes de abrir la base local, cierra cualquier otra instancia de esta familia
	// (incluida la misma versión) que siga ejecutándose en segundo plano. Esto evita
	// que el navegador termine conectado a otro EXE/carpeta y parezca que guardar o
	// eliminar no funciona porque en realidad se está viendo otra base local.
	stopRunningInstances()

	store, err := newStore(filepath.Join(dataDir, "sistema_notas.db.json"), uploads)
	if err != nil {
		showFatal("No se pudo abrir la base local: " + err.Error())
		return
	}

	listener, port, err := findListener()
	if err != nil {
		showFatal(err.Error())
		return
	}
	serverPort = port
	serverURLs = networkAddresses(port)
	recordServerStart(store)
	go autoCloseLoop(store)
	baseURL := fmt.Sprintf("http://%s:%d/?v=%s", loopbackHost, port, url.QueryEscape(appVersion))
	_ = os.WriteFile(filepath.Join(dataDir, "puerto.txt"), []byte(strconv.Itoa(port)), 0644)
	linkText := "ENLACE PARA LA SEGUNDA COMPUTADORA\r\n\r\n"
	if len(serverURLs) == 0 {
		linkText += "No se detectó una dirección de red. Verifica que ambas computadoras estén conectadas a la misma red.\r\n"
	} else {
		for _, address := range serverURLs {
			linkText += address + "\r\n"
		}
	}
	linkText += "\r\nLa computadora servidor debe permanecer encendida y con SistemaNotasServidor.exe abierto.\r\n"
	_ = os.WriteFile(filepath.Join(dataDir, "ENLACE_PARA_PC_2.txt"), []byte(linkText), 0644)

	server := &http.Server{
		Handler:           newHandler(store, backups),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		time.Sleep(650 * time.Millisecond)
		openBrowser(baseURL)
	}()
	log.Printf("Servidor iniciado en %s", baseURL)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("Servidor detenido con error: %v", err)
	}
}

func applicationDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func newStore(path, uploads string) (*Store, error) {
	s := &Store{path: path, uploads: uploads}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &s.db); err != nil {
			return nil, fmt.Errorf("base dañada: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if s.db.Version == 0 {
		s.db.Version = 1
	}
	if s.db.NextNoteID == 0 {
		s.db.NextNoteID = 1
	}
	if s.db.NextPhotoID == 0 {
		s.db.NextPhotoID = 1
	}
	if s.db.NextAuditID == 0 {
		s.db.NextAuditID = 1
	}
	now := time.Now()
	for i := range s.db.Notes {
		note := &s.db.Notes[i]
		if strings.TrimSpace(note.Operador) == "" {
			note.Operador = "SISTEMA"
		}
		if strings.TrimSpace(note.WorkflowStatus) == "" {
			// Las notas históricas no se cierran de golpe al instalar esta actualización.
			note.WorkflowStatus = statusOpen
			if created, err := time.Parse(time.RFC3339, note.CreatedAt); err == nil && now.Sub(created) >= 0 && now.Sub(created) < autoCloseAfter {
				note.AutoCloseEligible = true
			}
		}
		if note.WorkflowStatus == statusClosed || note.UsedAt != "" || note.PhotoOnly {
			note.AutoCloseEligible = false
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) addAuditLocked(action string, noteID int64, folio string, details map[string]any) {
	s.db.Audit = append(s.db.Audit, Audit{ID: s.db.NextAuditID, Action: action, NoteID: noteID, Folio: folio, Details: details, CreatedAt: nowISO()})
	s.db.NextAuditID++
	if len(s.db.Audit) > 5000 {
		s.db.Audit = append([]Audit(nil), s.db.Audit[len(s.db.Audit)-5000:]...)
	}
}

func newHandler(store *Store, backupDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"app":             appID,
			"family":          appFamily,
			"version":         appVersion,
			"serverPort":      serverPort,
			"serverUrls":      serverURLs,
			"localRequest":    requestIsLocal(r),
			"serverStartedAt": serverStartedAt,
			"serverHostName":  serverHostName,
			"serverOSUser":    serverOSUser,
		})
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		store.mu.RLock()
		defer store.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": store.db.Version, "count": len(store.db.Notes)})
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		loginDispatcher(w, r, store)
	})
	mux.HandleFunc("/api/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		listAudit(w, r, store)
	})
	mux.HandleFunc("/api/closure-codes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "codes": closureCatalog})
	})
	mux.HandleFunc("/api/closure-analysis", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		analyzeClosureHTTP(w, r, store)
	})
	mux.HandleFunc("/api/workflow", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		workflowAction(w, r, store)
	})
	mux.HandleFunc("/api/orthography", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		orthographyHTTP(w, r)
	})
	mux.HandleFunc("/api/notes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listNotes(w, r, store)
		case http.MethodPost:
			createNote(w, r, store)
		default:
			methodNotAllowed(w)
		}
	})
	mux.HandleFunc("/api/photo-only", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		createPhotoOnlyRecord(w, r, store)
	})
	mux.HandleFunc("/api/notes/", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(r.URL.Path, "/api/notes/")
		if !ok {
			writeError(w, http.StatusNotFound, "Nota no encontrada.")
			return
		}
		switch r.Method {
		case http.MethodGet:
			getNote(w, store, id)
		case http.MethodPut:
			updateNote(w, r, store, id)
		case http.MethodDelete:
			deleteNote(w, r, store, id)
		default:
			methodNotAllowed(w)
		}
	})
	mux.HandleFunc("/api/photos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		addPhoto(w, r, store)
	})
	mux.HandleFunc("/api/photos/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			methodNotAllowed(w)
			return
		}
		id, ok := parseID(r.URL.Path, "/api/photos/")
		if !ok {
			writeError(w, http.StatusNotFound, "Fotografía no encontrada.")
			return
		}
		deletePhoto(w, r, store, id)
	})
	mux.HandleFunc("/photos/", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(r.URL.Path, "/photos/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		servePhoto(w, r, store, id)
	})
	mux.HandleFunc("/api/backup", func(w http.ResponseWriter, r *http.Request) {
		createBackup(w, store, backupDir)
	})
	mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if !requestIsLocal(r) {
			writeError(w, http.StatusForbidden, "Solo la computadora servidor puede cerrar el sistema central.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		go func() {
			time.Sleep(250 * time.Millisecond)
			os.Exit(0)
		}()
	})

	staticFS, _ := fs.Sub(embeddedFiles, "web")
	fileServer := http.FileServer(http.FS(staticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/photos/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(staticFS, path); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		if strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}
		fileServer.ServeHTTP(w, r)
	})
	return withRecovery(mux)
}

func listNotes(w http.ResponseWriter, r *http.Request, s *Store) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	corp := strings.TrimSpace(r.URL.Query().Get("corporation"))
	s.mu.RLock()
	defer s.mu.RUnlock()
	countByNote := map[int64]int{}
	coverByNote := map[int64]int64{}
	for _, p := range s.db.Photos {
		countByNote[p.NoteID]++
		if coverByNote[p.NoteID] == 0 || p.ID < coverByNote[p.NoteID] {
			coverByNote[p.NoteID] = p.ID
		}
	}
	result := make([]noteSummary, 0, len(s.db.Notes))
	for _, n := range s.db.Notes {
		if corp != "" && n.Corporacion != corp {
			continue
		}
		if q != "" {
			haystack := strings.ToLower(n.Folio + " " + n.Titulo + " " + n.Municipio + " " + stripHTML(n.ContenidoHTML))
			if !strings.Contains(haystack, q) {
				continue
			}
		}
		result = append(result, noteSummary{Note: n, PhotoCount: countByNote[n.ID], CoverPhotoID: coverByNote[n.ID]})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FechaClave != result[j].FechaClave {
			return result[i].FechaClave > result[j].FechaClave
		}
		return result[i].ID > result[j].ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "notes": result, "version": s.db.Version})
}

func getNote(w http.ResponseWriter, s *Store, id int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.db.Notes {
		if n.ID != id {
			continue
		}
		photos := []photoPublic{}
		var cover int64
		for _, p := range s.db.Photos {
			if p.NoteID == id {
				if cover == 0 || p.ID < cover {
					cover = p.ID
				}
				photos = append(photos, photoPublic{ID: p.ID, Name: p.Name, Mime: p.Mime, Size: p.Size, CreatedAt: p.CreatedAt, URL: fmt.Sprintf("/photos/%d", p.ID)})
			}
		}
		sort.Slice(photos, func(i, j int) bool { return photos[i].ID < photos[j].ID })
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": fullNote{Note: n, PhotoCount: len(photos), CoverPhotoID: cover, Photos: photos}, "version": s.db.Version})
		return
	}
	writeError(w, http.StatusNotFound, "La nota no existe.")
}

func createNote(w http.ResponseWriter, r *http.Request, s *Store) {
	operator := dispatcherFromRequest(r)
	if operator == "" {
		writeError(w, http.StatusUnauthorized, "Inicia sesión de despacho antes de registrar una nota.")
		return
	}
	var input Note
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.Operador = operator
	validated, err := validateNote(input, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.db.Notes {
		if n.Folio == validated.Folio {
			writeError(w, http.StatusConflict, "El folio ya existe. Utilice otra terminación.")
			return
		}
	}
	now := nowISO()
	validated.ID = s.db.NextNoteID
	s.db.NextNoteID++
	validated.CreatedAt = now
	validated.UpdatedAt = now
	validated.WorkflowStatus = statusNew
	validated.AutoCloseEligible = true
	s.db.Notes = append(s.db.Notes, validated)
	s.addAuditLocked("CREAR_NOTA", validated.ID, validated.Folio, map[string]any{
		"corporacion": validated.Corporacion,
		"operador":    operator,
		"ip":          clientIP(r),
	})
	s.db.Version++
	if err := s.saveLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "id": validated.ID, "folio": validated.Folio,
		"version": s.db.Version, "orthographyCorrected": validated.OrthographyCorrected,
	})
}

func createPhotoOnlyRecord(w http.ResponseWriter, r *http.Request, s *Store) {
	operator := dispatcherFromRequest(r)
	if operator == "" {
		writeError(w, http.StatusUnauthorized, "Inicia sesión de despacho antes de registrar fotografías.")
		return
	}
	var payload struct {
		Corporacion string `json:"corporacion"`
		Titulo      string `json:"titulo"`
		Municipio   string `json:"municipio"`
		Referencia  string `json:"referencia"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	corporacion := truncate(strings.TrimSpace(payload.Corporacion), 20)
	titulo := truncate(correctPlainText(strings.TrimSpace(payload.Titulo)), 160)
	municipio := truncate(strings.TrimSpace(payload.Municipio), 100)
	referencia := truncate(correctPlainText(strings.TrimSpace(payload.Referencia)), 500)
	if corporacion == "" {
		writeError(w, http.StatusBadRequest, "Seleccione la corporación que proporcionó las fotografías.")
		return
	}
	if titulo == "" {
		writeError(w, http.StatusBadRequest, "Seleccione el nombre del incidente para identificar las fotografías.")
		return
	}
	if municipio == "" {
		writeError(w, http.StatusBadRequest, "Seleccione el municipio.")
		return
	}
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	dateKey := now.Format("20060102")

	s.mu.Lock()
	id := s.db.NextNoteID
	s.db.NextNoteID++
	folio := fmt.Sprintf("FOTO/%s/%06d", dateKey, id)
	content := ""
	if referencia != "" {
		content = "<p>" + html.EscapeString(referencia) + "</p>"
	}
	note := Note{
		ID: id, Folio: folio, FolioManual: fmt.Sprintf("%06d", id), FechaClave: dateKey,
		Titulo: titulo, Corporacion: corporacion, Municipio: municipio,
		Operador: operator, ContenidoHTML: content, WorkflowStatus: statusOpen,
		OpenedAt: nowText, AutoCloseEligible: false, OrthographyCorrected: referencia != strings.TrimSpace(payload.Referencia),
		PhotoOnly: true, CreatedAt: nowText, UpdatedAt: nowText,
	}
	s.db.Notes = append(s.db.Notes, note)
	s.addAuditLocked("CREAR_REGISTRO_FOTOS", note.ID, note.Folio, map[string]any{
		"corporacion": corporacion, "titulo": titulo, "operador": operator, "ip": clientIP(r),
	})
	s.db.Version++
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	version := s.db.Version
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "id": note.ID, "folio": note.Folio, "version": version,
	})
}

func updateNote(w http.ResponseWriter, r *http.Request, s *Store, id int64) {
	operator := dispatcherFromRequest(r)
	if operator == "" {
		writeError(w, http.StatusUnauthorized, "Inicia sesión de despacho antes de editar una nota.")
		return
	}
	var input Note
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.db.Notes {
		if s.db.Notes[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "La nota no existe.")
		return
	}
	old := s.db.Notes[index]
	input.Operador = old.Operador
	validated, err := validateNote(input, old.FechaClave)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, n := range s.db.Notes {
		if n.ID != id && n.Folio == validated.Folio {
			writeError(w, http.StatusConflict, "El folio ya existe. Utilice otra terminación.")
			return
		}
	}
	validated.ID = id
	validated.CreatedAt = old.CreatedAt
	validated.UpdatedAt = nowISO()
	validated.Operador = old.Operador
	validated.WorkflowStatus = old.WorkflowStatus
	validated.OpenedAt = old.OpenedAt
	validated.UsedAt = old.UsedAt
	validated.ClosedAt = old.ClosedAt
	validated.ClosureCode = old.ClosureCode
	validated.ClosureName = old.ClosureName
	validated.ClosureMethod = old.ClosureMethod
	validated.ClosureReason = old.ClosureReason
	validated.AutoCloseEligible = old.AutoCloseEligible
	validated.PhotoOnly = old.PhotoOnly
	validated.OrthographyCorrected = validated.OrthographyCorrected || old.OrthographyCorrected
	s.db.Notes[index] = validated
	s.addAuditLocked("EDITAR_NOTA", id, validated.Folio, map[string]any{
		"folioAnterior": old.Folio,
		"operador":      operator,
		"ip":            clientIP(r),
	})
	s.db.Version++
	if err := s.saveLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": id, "folio": validated.Folio, "version": s.db.Version,
		"orthographyCorrected": validated.OrthographyCorrected,
	})
}

func deleteNote(w http.ResponseWriter, r *http.Request, s *Store, id int64) {
	s.mu.Lock()
	index := -1
	var folio string
	for i, n := range s.db.Notes {
		if n.ID == id {
			index, folio = i, n.Folio
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "La nota no existe.")
		return
	}
	files := []string{}
	keptPhotos := s.db.Photos[:0]
	for _, p := range s.db.Photos {
		if p.NoteID == id {
			files = append(files, p.StoredName)
		} else {
			keptPhotos = append(keptPhotos, p)
		}
	}
	s.db.Photos = keptPhotos
	s.db.Notes = append(s.db.Notes[:index], s.db.Notes[index+1:]...)
	s.addAuditLocked("ELIMINAR_NOTA", id, folio, map[string]any{"operador": dispatcherOrSystem(r), "ip": clientIP(r)})
	s.db.Version++
	err := s.saveLocked()
	version := s.db.Version
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, file := range files {
		_ = os.Remove(filepath.Join(s.uploads, file))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
}

func addPhoto(w http.ResponseWriter, r *http.Request, s *Store) {
	noteID, err := strconv.ParseInt(r.URL.Query().Get("note_id"), 10, 64)
	if err != nil || noteID <= 0 {
		writeError(w, http.StatusBadRequest, "Identificador de nota inválido.")
		return
	}
	name, _ := url.QueryUnescape(r.URL.Query().Get("name"))
	if name == "" {
		name = "imagen"
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}[contentType]
	if ext == "" {
		writeError(w, http.StatusBadRequest, "Solo se permiten imágenes JPG, PNG, WEBP o GIF.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "La imagen está vacía o supera 20 MB.")
		return
	}

	s.mu.Lock()
	var folio string
	exists := false
	count := 0
	for _, n := range s.db.Notes {
		if n.ID == noteID {
			exists, folio = true, n.Folio
			break
		}
	}
	for _, p := range s.db.Photos {
		if p.NoteID == noteID {
			count++
		}
	}
	if !exists {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "La nota no existe.")
		return
	}
	if count >= maxPhotos {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, fmt.Sprintf("La nota admite un máximo de %d fotografías.", maxPhotos))
		return
	}
	stored := fmt.Sprintf("%d_%d_%d%s", noteID, time.Now().UnixNano(), s.db.NextPhotoID, ext)
	if err := os.WriteFile(filepath.Join(s.uploads, stored), data, 0644); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := Photo{ID: s.db.NextPhotoID, NoteID: noteID, StoredName: stored, Name: truncate(name, 180), Mime: contentType, Size: int64(len(data)), CreatedAt: nowISO()}
	s.db.NextPhotoID++
	s.db.Photos = append(s.db.Photos, p)
	s.addAuditLocked("AGREGAR_FOTO", noteID, folio, map[string]any{"photoId": p.ID, "size": len(data), "operador": dispatcherOrSystem(r), "ip": clientIP(r)})
	s.db.Version++
	err = s.saveLocked()
	version := s.db.Version
	s.mu.Unlock()
	if err != nil {
		_ = os.Remove(filepath.Join(s.uploads, stored))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "photoId": p.ID, "url": fmt.Sprintf("/photos/%d", p.ID), "version": version})
}

func deletePhoto(w http.ResponseWriter, r *http.Request, s *Store, id int64) {
	s.mu.Lock()
	index := -1
	var photo Photo
	var folio string
	for i, p := range s.db.Photos {
		if p.ID == id {
			index, photo = i, p
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "La fotografía no existe.")
		return
	}
	for _, n := range s.db.Notes {
		if n.ID == photo.NoteID {
			folio = n.Folio
			break
		}
	}
	s.db.Photos = append(s.db.Photos[:index], s.db.Photos[index+1:]...)
	s.addAuditLocked("ELIMINAR_FOTO", photo.NoteID, folio, map[string]any{"photoId": id, "operador": dispatcherOrSystem(r), "ip": clientIP(r)})
	s.db.Version++
	err := s.saveLocked()
	version := s.db.Version
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = os.Remove(filepath.Join(s.uploads, photo.StoredName))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
}

func servePhoto(w http.ResponseWriter, r *http.Request, s *Store, id int64) {
	s.mu.RLock()
	var photo *Photo
	for i := range s.db.Photos {
		if s.db.Photos[i].ID == id {
			copy := s.db.Photos[i]
			photo = &copy
			break
		}
	}
	s.mu.RUnlock()
	if photo == nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.uploads, photo.StoredName)
	w.Header().Set("Content-Type", photo.Mime)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Disposition", "inline; filename=\""+safeHeaderFilename(photo.Name)+"\"")
	http.ServeFile(w, r, path)
}

func createBackup(w http.ResponseWriter, s *Store, backupDir string) {
	stamp := time.Now().Format("20060102_150405")
	name := "respaldo_notas_" + stamp + ".zip"
	path := filepath.Join(backupDir, name)
	file, err := os.Create(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zw := zip.NewWriter(file)
	s.mu.RLock()
	dbData, _ := json.MarshalIndent(s.db, "", "  ")
	photos := append([]Photo(nil), s.db.Photos...)
	s.mu.RUnlock()
	entry, _ := zw.Create("datos/sistema_notas.db.json")
	_, _ = entry.Write(dbData)
	for _, p := range photos {
		data, err := os.ReadFile(filepath.Join(s.uploads, p.StoredName))
		if err != nil {
			continue
		}
		entry, _ := zw.Create("datos/uploads/" + p.StoredName)
		_, _ = entry.Write(data)
	}
	entry, _ = zw.Create("LEEME.txt")
	_, _ = entry.Write([]byte("Respaldo del Sistema de Notas Informativas.\r\nCreado: " + nowISO() + "\r\n"))
	_ = zw.Close()
	_ = file.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	info, statErr := os.Stat(path)
	if statErr != nil {
		writeError(w, http.StatusInternalServerError, statErr.Error())
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	f, openErr := os.Open(path)
	if openErr != nil {
		writeError(w, http.StatusInternalServerError, openErr.Error())
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

func validateNote(input Note, existingDate string) (Note, error) {
	manual := normalizeManual(input.FolioManual)
	dateKey := nonDigits.ReplaceAllString(input.FechaClave, "")
	if existingDate != "" {
		dateKey = existingDate
	}
	originalTitle := strings.TrimSpace(input.Titulo)
	input.Titulo = truncate(correctPlainText(originalTitle), 160)
	input.Corporacion = truncate(strings.TrimSpace(input.Corporacion), 20)
	input.Municipio = truncate(strings.TrimSpace(input.Municipio), 100)
	input.Operador = truncate(strings.TrimSpace(input.Operador), 100)
	if input.Operador == "" {
		input.Operador = "SISTEMA"
	}
	originalHTML := sanitizeHTML(input.ContenidoHTML)
	input.ContenidoHTML = normalizeNoteStructureHTML(correctHTMLOrthography(originalHTML))
	input.OrthographyCorrected = input.Titulo != originalTitle || input.ContenidoHTML != originalHTML
	if len(dateKey) != 8 {
		return Note{}, errors.New("La fecha de la nota no es válida.")
	}
	if manual == "" || strings.Trim(manual, "0") == "" {
		return Note{}, errors.New("Escriba la terminación del folio.")
	}
	if input.Titulo == "" {
		return Note{}, errors.New("Escriba el título del incidente.")
	}
	if input.Corporacion == "" {
		return Note{}, errors.New("Seleccione una corporación.")
	}
	if input.Municipio == "" {
		return Note{}, errors.New("Escriba el municipio.")
	}
	if len(strings.TrimSpace(stripHTML(input.ContenidoHTML))) < 5 {
		return Note{}, errors.New("Escriba el contenido de la nota informativa.")
	}
	input.FolioManual = manual
	input.FechaClave = dateKey
	input.Folio = "REF/" + dateKey + "/" + manual
	return input, nil
}

func sanitizeHTML(value string) string {
	value = dangerousBlock.ReplaceAllString(value, "")
	allowed := map[string]bool{"p": true, "br": true, "b": true, "strong": true, "i": true, "em": true, "u": true, "ul": true, "ol": true, "li": true, "div": true}
	return strings.TrimSpace(allowedTag.ReplaceAllStringFunc(value, func(tagText string) string {
		m := allowedTag.FindStringSubmatch(tagText)
		if len(m) < 3 {
			return ""
		}
		tag := strings.ToLower(m[2])
		if !allowed[tag] {
			return ""
		}
		if tag == "br" {
			return "<br>"
		}
		if m[1] == "/" {
			return "</" + tag + ">"
		}
		return "<" + tag + ">"
	}))
}

func stripHTML(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(stripTags.ReplaceAllString(value, " "))), " ")
}

func normalizeManual(value string) string {
	digits := nonDigits.ReplaceAllString(value, "")
	if len(digits) > 20 {
		digits = digits[:20]
	}
	return digits
}

func parseID(path, prefix string) (int64, bool) {
	value := strings.TrimPrefix(path, prefix)
	if value == path || strings.Contains(value, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func decodeJSON(r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(target); err != nil {
		return errors.New("Los datos enviados no son válidos.")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "Método no permitido.")
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

func truncate(value string, max int) string {
	r := []rune(value)
	if len(r) > max {
		return string(r[:max])
	}
	return value
}

func safeHeaderFilename(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\"", "'")
	value = strings.TrimSpace(value)
	if value == "" {
		return "imagen"
	}
	return truncate(value, 160)
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(r.RemoteAddr, "[]")
}

func dispatcherFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	value := strings.TrimSpace(r.Header.Get("X-Dispatcher"))
	value = strings.Join(strings.Fields(value), " ")
	return truncate(value, 80)
}

func dispatcherOrSystem(r *http.Request) string {
	if value := dispatcherFromRequest(r); value != "" {
		return value
	}
	return "SISTEMA"
}

func loginDispatcher(w http.ResponseWriter, r *http.Request, s *Store) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(payload.Name)), " "))
	name = truncate(name, 80)
	if len([]rune(name)) < 2 {
		writeError(w, http.StatusBadRequest, "Escribe el nombre o clave del despachador.")
		return
	}
	s.mu.Lock()
	s.addAuditLocked("INICIAR_SESION", 0, "", map[string]any{
		"operador":  name,
		"ip":        clientIP(r),
		"navegador": truncate(r.UserAgent(), 180),
	})
	s.db.Version++
	err := s.saveLocked()
	version := s.db.Version
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "version": version})
}

func listAudit(w http.ResponseWriter, r *http.Request, s *Store) {
	limit := 80
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			if value > 300 {
				value = 300
			}
			limit = value
		}
	}
	s.mu.RLock()
	start := len(s.db.Audit) - limit
	if start < 0 {
		start = 0
	}
	items := append([]Audit(nil), s.db.Audit[start:]...)
	s.mu.RUnlock()
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"audit":           items,
		"serverStartedAt": serverStartedAt,
		"serverHostName":  serverHostName,
		"serverOSUser":    serverOSUser,
	})
}

func recordServerStart(s *Store) {
	serverStartedAt = nowISO()
	serverHostName, _ = os.Hostname()
	serverOSUser = strings.TrimSpace(os.Getenv("USERNAME"))
	if serverOSUser == "" {
		serverOSUser = strings.TrimSpace(os.Getenv("USER"))
	}
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		serverOSUser = current.Username
	}
	if serverOSUser == "" {
		serverOSUser = "USUARIO DESCONOCIDO"
	}
	if serverHostName == "" {
		serverHostName = "PC DESCONOCIDA"
	}
	s.mu.Lock()
	s.addAuditLocked("INICIAR_SERVIDOR", 0, "", map[string]any{
		"usuarioSO": serverOSUser,
		"pc":        serverHostName,
		"puerto":    serverPort,
		"version":   appVersion,
	})
	s.db.Version++
	_ = s.saveLocked()
	s.mu.Unlock()
}

func orthographyHTTP(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Title string `json:"title"`
		HTML  string `json:"html"`
		Text  string `json:"text"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	correctedTitle := correctPlainText(payload.Title)
	correctedHTML := normalizeNoteStructureHTML(correctHTMLOrthography(payload.HTML))
	correctedText := correctPlainText(payload.Text)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"title":   correctedTitle,
		"html":    correctedHTML,
		"text":    correctedText,
		"changed": correctedTitle != payload.Title || correctedHTML != payload.HTML || correctedText != payload.Text,
	})
}

func loadEmbeddedCatalogs() {
	closureCatalog = []ClosureCode{}
	closureByCode = map[string]ClosureCode{}
	if data, err := embeddedFiles.ReadFile("closure_codes.json"); err == nil {
		_ = json.Unmarshal(data, &closureCatalog)
	}
	for _, item := range closureCatalog {
		closureByCode[item.Code] = item
	}

	incidentCatalog = []IncidentTipification{}
	if data, err := embeddedFiles.ReadFile("web/tipificaciones.json"); err == nil {
		var catalog IncidentCatalogFile
		if json.Unmarshal(data, &catalog) == nil {
			incidentCatalog = catalog.Items
		}
	}

	corrections := map[string]string{}
	if data, err := embeddedFiles.ReadFile("corrections.json"); err == nil {
		_ = json.Unmarshal(data, &corrections)
	}
	keys := make([]string, 0, len(corrections))
	for key := range corrections {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return len([]rune(keys[i])) > len([]rune(keys[j]))
	})
	orthographyRules = orthographyRules[:0]
	for _, key := range keys {
		replacement := strings.TrimSpace(corrections[key])
		if replacement == "" {
			continue
		}
		pattern := regexp.MustCompile(`(?i)(^|[^\p{L}])(` + regexp.QuoteMeta(key) + `)([^\p{L}]|$)`)
		orthographyRules = append(orthographyRules, CorrectionRule{Key: key, Replacement: replacement, Pattern: pattern})
	}

	vocab := map[string]int{}
	if data, err := embeddedFiles.ReadFile("orthography_vocab.json"); err == nil {
		_ = json.Unmarshal(data, &vocab)
	}
	buildOrthographyLexicon(corrections, vocab)
}

func orthographyKey(value string) string {
	return strings.ToLower(normalizeClosureText(value))
}

func addLexiconWordFreq(word string, freq int) {
	word = strings.TrimSpace(strings.ToLower(word))
	if len([]rune(word)) < 3 {
		return
	}
	key := orthographyKey(word)
	if key == "" || strings.Contains(key, " ") {
		return
	}
	values := orthographyLexicon[key]
	for _, existing := range values {
		if existing == word {
			if freq > 0 {
				orthographyFrequency[key] += freq
			}
			return
		}
	}
	orthographyLexicon[key] = append(values, word)
	if freq <= 0 {
		freq = 1
	}
	orthographyFrequency[key] += freq
}

func addLexiconWord(word string) { addLexiconWordFreq(word, 1) }

func addLexiconText(text string) {
	for _, word := range wordPattern.FindAllString(text, -1) {
		addLexiconWordFreq(word, 2)
	}
}

func buildOrthographyLexicon(corrections map[string]string, vocab map[string]int) {
	orthographyLexicon = map[string][]string{}
	orthographyByInitial = map[rune][]string{}
	orthographyByLength = map[int][]string{}
	orthographyFrequency = map[string]int{}
	for word, freq := range vocab {
		addLexiconWordFreq(word, freq)
	}
	for _, replacement := range corrections {
		addLexiconText(replacement)
	}
	for _, item := range incidentCatalog {
		addLexiconText(item.Name)
		addLexiconText(item.Type)
		addLexiconText(item.Subtype)
	}
	for _, item := range closureCatalog {
		addLexiconText(item.Name)
		addLexiconText(item.Definition)
	}
	// Vocabulario operativo frecuente que aparece en notas informativas. Todo queda local/offline.
	addLexiconText(`arribo arribar arribaron acudió acudieron atención atendió atendida atendido valorar valoró valoración signos vitales estabilización estabilizó primeros auxilios prehospitalaria paramédico paramédica ambulancia traslado trasladó trasladada trasladado hospital clínica institución salud paciente persona masculino femenino aproximadamente posteriormente inmediatamente domicilio dirección colonia municipio localidad referencia unidad móvil motopatrulla patrulla elemento elementos comandante supervisor operador despacho reporte reportante responsable afectada afectado lesionada lesionado lesiones inconsciente consciente emergencia incidente hechos lugar sitio apoyo solicitud solicitó informó manifestó indicó verificó localizó encontró desconocido desconocida fuego incendio llamas humo bodega vivienda comercio pastizal basura controlado sofocado extinguido fuga derrame combustible gasolina vehículo motocicleta automóvil conductor conductores tránsito vialidad accidente percance aseguradora aseguradoras convenio acuerdo particulares daños corralón infracción fiscalía ministerio público disposición detenido detenida flagrancia persecución orientación información canalización instancia competente prevención preventivo vigilancia rondín seguridad protección civil policía municipal estatal bomberos rescate rescatada rescatado evacuación evacuada evacuado acordonamiento perímetro riesgo población ciudadanía institución hospitalaria falleció fallecida fallecido deceso occisa occiso semefo albergue refugio negativa negó tratamiento asesoría telefónica alarma bancaria banco seproban grabación aplicativo monitoreo seguimiento consigna inteligencia investigación localización desaparecida localizado localizada`)
	// Palabras españolas válidas y frecuentes que se protegen para evitar falsas correcciones.
	addLexiconText(`media medio medios hora horas minuto minutos antes después durante cada general generales parte partes estado estados base bases calle calles avenida avenidas zona zonas ejido ranchería carretera camino punto puntos personal servicio servicios salida entradas entrada regreso tiempo día días noche mañana tarde momento momentos forma manera área áreas domicilio domicilios casa casas lugar lugares sitio sitios nombre nombres datos dato cargo cargos tipo tipos jefe jefa turno turnos apoyo apoyos reporte reportes ciudadano ciudadana ciudadanos ciudadanas vehículo vehículos unidad unidades móvil móviles radio radios orden órdenes daños material materiales particular particulares público pública públicos públicas`)
	for key := range orthographyLexicon {
		runes := []rune(key)
		if len(runes) == 0 {
			continue
		}
		orthographyByInitial[runes[0]] = append(orthographyByInitial[runes[0]], key)
		orthographyByLength[len(runes)] = append(orthographyByLength[len(runes)], key)
	}
}

func damerauDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			v := d[i-1][j] + 1
			if x := d[i][j-1] + 1; x < v {
				v = x
			}
			if x := d[i-1][j-1] + cost; x < v {
				v = x
			}
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if x := d[i-2][j-2] + 1; x < v {
					v = x
				}
			}
			d[i][j] = v
		}
	}
	return d[len(ra)][len(rb)]
}

func sameLetters(value string) string {
	runes := []rune(value)
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	return string(runes)
}

func safeTypoShape(input, candidate string) bool {
	if input == candidate {
		return false
	}
	a := []rune(input)
	b := []rune(candidate)
	diff := absInt(len(a) - len(b))
	if diff > 2 {
		return false
	}
	if diff <= 1 {
		return true
	}
	// Dos letras de diferencia sólo se aceptan en palabras suficientemente largas.
	return len(a) >= 7 && len(b) >= 7
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func maxTypoDistance(length int) int {
	if length <= 5 {
		return 1
	}
	if length <= 7 {
		return 1
	}
	return 2
}

func fuzzyCorrectWord(original string) string {
	key := orthographyKey(original)
	if key == "" || strings.Contains(key, " ") || len([]rune(key)) < 3 {
		return original
	}
	if variants := orthographyLexicon[key]; len(variants) == 1 {
		canonical := variants[0]
		if strings.EqualFold(original, canonical) {
			return original
		}
		return applyCaseStyle(original, canonical)
	} else if len(variants) > 1 {
		return original
	}

	letters := []rune(original)
	if len(letters) > 1 && unicode.IsUpper(letters[0]) {
		allUpper := true
		for _, r := range letters[1:] {
			if unicode.IsLetter(r) && !unicode.IsUpper(r) {
				allUpper = false
				break
			}
		}
		if !allUpper {
			return original
		} // proteger nombres propios
	}

	kr := []rune(key)
	maxDist := maxTypoDistance(len(kr))
	candidateSet := map[string]struct{}{}
	if len(kr) > 0 {
		for _, c := range orthographyByInitial[kr[0]] {
			candidateSet[c] = struct{}{}
		}
	}
	// Para palabras de 6+ letras también considerar primera letra equivocada/omitida.
	if len(kr) >= 6 {
		for l := len(kr) - maxDist; l <= len(kr)+maxDist; l++ {
			for _, c := range orthographyByLength[l] {
				candidateSet[c] = struct{}{}
			}
		}
	}
	type scored struct {
		key  string
		dist int
		freq int
	}
	best := scored{dist: 99}
	second := scored{dist: 99}
	for candidateKey := range candidateSet {
		if !safeTypoShape(key, candidateKey) {
			continue
		}
		dist := damerauDistance(key, candidateKey)
		if dist > maxDist {
			continue
		}
		freq := orthographyFrequency[candidateKey]
		cur := scored{candidateKey, dist, freq}
		better := func(a, b scored) bool {
			if a.dist != b.dist {
				return a.dist < b.dist
			}
			return a.freq > b.freq
		}
		if better(cur, best) {
			second = best
			best = cur
		} else if better(cur, second) {
			second = cur
		}
	}
	if best.key == "" || best.dist > maxDist {
		return original
	}
	// En distancia 2 exigimos una ventaja real para evitar cambiar palabras válidas.
	if best.dist == 2 {
		if second.key != "" && second.dist == best.dist && best.freq < second.freq*3 {
			return original
		}
		if best.freq < 2 {
			return original
		}
	} else if second.key != "" && second.dist == best.dist && best.freq == second.freq {
		return original
	}
	variants := orthographyLexicon[best.key]
	if len(variants) != 1 {
		return original
	}
	return applyCaseStyle(original, variants[0])
}

func applyCaseStyle(original, replacement string) string {
	letters := []rune(original)
	allUpper := true
	allLower := true
	seenLetter := false
	for _, r := range letters {
		if !unicode.IsLetter(r) {
			continue
		}
		seenLetter = true
		if unicode.IsLower(r) {
			allUpper = false
		}
		if unicode.IsUpper(r) {
			allLower = false
		}
	}
	if seenLetter && allUpper {
		return strings.ToUpper(replacement)
	}
	if seenLetter && allLower {
		return strings.ToLower(replacement)
	}
	if len(letters) > 0 && unicode.IsUpper(letters[0]) {
		runes := []rune(strings.ToLower(replacement))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		return string(runes)
	}
	return replacement
}

func correctPlainText(value string) string {
	result := value
	for _, rule := range orthographyRules {
		result = rule.Pattern.ReplaceAllStringFunc(result, func(match string) string {
			parts := rule.Pattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + applyCaseStyle(parts[2], rule.Replacement) + parts[3]
		})
	}
	// Segunda capa: restaura acentos inequívocos y corrige transposiciones/letras duplicadas
	// usando el vocabulario local ampliado del CNIE, códigos de cierre y español operativo.
	result = wordPattern.ReplaceAllStringFunc(result, fuzzyCorrectWord)
	// Tercera capa: vuelve a aplicar reglas de frase. Esto corrige casos encadenados,
	// por ejemplo ATENCIO MEDIA -> ATENCIÓN MEDIA -> ATENCIÓN MÉDICA.
	for _, rule := range orthographyRules {
		result = rule.Pattern.ReplaceAllStringFunc(result, func(match string) string {
			parts := rule.Pattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + applyCaseStyle(parts[2], rule.Replacement) + parts[3]
		})
	}
	return result
}

func correctHTMLOrthography(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	var out strings.Builder
	start := 0
	for _, loc := range stripTags.FindAllStringIndex(value, -1) {
		if loc[0] > start {
			out.WriteString(correctPlainText(value[start:loc[0]]))
		}
		out.WriteString(value[loc[0]:loc[1]])
		start = loc[1]
	}
	if start < len(value) {
		out.WriteString(correctPlainText(value[start:]))
	}
	return out.String()
}

func normalizeNoteStructureHTML(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	// Normalización visual segura para notas pegadas desde WhatsApp u otros sistemas.
	// No cambia el orden ni el significado: solo unifica bloques y marcadores *texto*.
	value = regexp.MustCompile(`(?is)<\s*div\s*>`).ReplaceAllString(value, "<p>")
	value = regexp.MustCompile(`(?is)</\s*div\s*>`).ReplaceAllString(value, "</p>")
	whatsappBold := regexp.MustCompile(`\*([^*<>\n]{1,220})\*`)
	value = whatsappBold.ReplaceAllString(value, "<strong>$1</strong>")
	// Evita huecos excesivos que dificultan identificar el resultado final del incidente.
	excessBreaks := regexp.MustCompile(`(?is)(?:<\s*br\s*/?\s*>\s*){4,}`)
	value = excessBreaks.ReplaceAllString(value, "<br><br>")
	emptyParas := regexp.MustCompile(`(?is)<p>\s*(?:&nbsp;|<br\s*/?>|\s)*</p>`)
	value = emptyParas.ReplaceAllString(value, "")
	return strings.TrimSpace(value)
}

func plainNoteLines(value string) []string {
	value = htmlLineBreaks.ReplaceAllString(value, "\n")
	value = stripTags.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, "\r", "\n")
	parts := strings.Split(value, "\n")
	lines := make([]string, 0, len(parts))
	for _, line := range parts {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "*#•- \t")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func isMetadataClosureLine(normalized string) bool {
	prefixes := []string{
		"SECRETARIA ", "SECRETARIA MUNICIPAL", "FECHA ", "FECHA:", "HORA ", "HORA:",
		"SOLICITA ", "SOLICITA:", "AMBULANCIA ", "AMBULANCIA:", "JEFE DE SERVICIO", "TIPO DE SERVICIO",
		"CRONOMETRIA", "AVISO ", "AVISO:", "LLEGADA AL LUGAR", "SALIDA DE BASE", "SALIDA DEL LUGAR",
		"REGRESO A BASE", "UNIDAD ", "UNIDAD:", "FOLIO ", "FOLIO:", "MUNICIPIO ", "MUNICIPIO:",
		"DATOS DEL PACIENTE", "NOMBRE ", "NOMBRE:", "DOMICILIO ", "DOMICILIO:", "EDAD ", "EDAD:",
		"FAMILIAR ", "FAMILIAR:", "OCUPACION ", "OCUPACION:", "PERTENENCIAS ", "PERTENENCIAS:",
		"RECIBE ", "RECIBE:", "ATIENDE ", "ATIENDE:", "OPERADOR ", "OPERADOR:",
		"ALERGIAS ", "ALERGIAS:", "MEDICAMENTOS ", "MEDICAMENTOS:", "PADECIMIENTOS ", "PADECIMIENTOS:",
		"ULTIMO LUNCH", "EVENTO PREVIOS", "EVENTOS PREVIOS",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func isOutcomeMarker(normalized string) bool {
	markers := []string{"RESULTADO", "CIERRE", "CONCLUSION", "OBSERVACIONES", "ACCIONES REALIZADAS", "ATENCION BRINDADA", "SITUACION FINAL", "NOVEDADES"}
	for _, marker := range markers {
		if normalized == marker || strings.HasPrefix(normalized, marker+" ") {
			return true
		}
	}
	return false
}

func hasOutcomeVerb(normalized string) bool {
	needles := []string{
		"ATEND", "VALOR", "TRASLAD", "FALLEC", "SIN SIGNOS VITALES", "NEG", "RECHAZ", "RESCAT", "EVACU",
		"DETEN", "DISPOSICION", "FUGA", "REMIT", "CORRALON", "INFRACCION", "CONVENIO", "ACUERDO", "ASEGURAD",
		"CONTROL", "SOFOC", "EXTING", "LOCALIZ", "NO SE LOCALIZ", "NO SE HIZO CONTACTO", "ORIENT", "CANALIZ",
		"INFORMA A LA CORPORACION", "NOTIFICO A LA CORPORACION", "ACORDON", "INTELIGENCIA", "SIMULACRO", "CONSIGNA",
		"MONITOREO", "SEPROBAN", "ALARMA", "COMBUSTIBLE", "FALTA DE UNIDADES", "ORDEN SUPERIOR", "SEMEFO", "ALBERGUE",
	}
	for _, needle := range needles {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func uniqueAppend(list []string, seen map[string]bool, value string) []string {
	key := normalizeClosureText(value)
	if key == "" || seen[key] {
		return list
	}
	seen[key] = true
	return append(list, value)
}

func closureTextSections(note Note) (full string, tail string, outcome string) {
	lines := plainNoteLines(note.ContenidoHTML)
	narrative := make([]string, 0, len(lines))
	outcomeLines := []string{}
	seenOutcome := map[string]bool{}
	inOutcome := false
	for _, line := range lines {
		n := normalizeClosureText(line)
		if n == "" {
			continue
		}
		if isOutcomeMarker(n) {
			inOutcome = true
			// No descartamos la misma línea: muchas notas escriben
			// "RESULTADO FINAL: ..." y el resultado viene a continuación del marcador.
		}
		if isMetadataClosureLine(n) {
			continue
		}
		narrative = append(narrative, line)
		if inOutcome || hasOutcomeVerb(n) {
			outcomeLines = uniqueAppend(outcomeLines, seenOutcome, line)
		}
	}
	// Las definiciones del catálogo de cierre se basan en las últimas notas: siempre ponderar
	// también las últimas líneas narrativas aunque no traigan una etiqueta formal de resultado.
	start := len(narrative) - 7
	if start < 0 {
		start = 0
	}
	for _, line := range narrative[start:] {
		outcomeLines = uniqueAppend(outcomeLines, seenOutcome, line)
	}
	full = normalizeClosureText(strings.Join(narrative, " "))
	tail = tailRunes(full, 3400)
	outcome = normalizeClosureText(strings.Join(outcomeLines, " "))
	outcome = tailRunes(outcome, 2200)
	if outcome == "" {
		outcome = tailRunes(tail, 1400)
	}
	return full, tail, outcome
}

func directStrongClosure(outcome, tail string) (string, string) {
	bestCode := ""
	bestPhrase := ""
	bestWeight := 0
	commonSingles := map[string]bool{"APOYO": true, "FUGA": true, "RESCATE": true, "CONVENIO": true, "EVACUACION": true, "TRASLADO": true, "ATENCION": true, "UNIDAD": true, "ALARMA": true}
	for _, item := range closureCatalog {
		for _, phrase := range item.Strong {
			needle := normalizeClosureText(phrase)
			if needle == "" {
				continue
			}
			words := len(strings.Fields(needle))
			if words == 1 && commonSingles[needle] {
				continue
			}
			weight := len([]rune(needle)) + words*12
			matched := strings.Contains(outcome, needle)
			if !matched && words >= 2 {
				matched = strings.Contains(tail, needle)
				weight -= 12
			}
			if matched && weight > bestWeight {
				bestWeight = weight
				bestCode = item.Code
				bestPhrase = phrase
			}
		}
	}
	if bestCode != "" && bestWeight >= 28 {
		return bestCode, "COINCIDENCIA DIRECTA EN EL RESULTADO DE LA NOTA: " + bestPhrase
	}
	return "", ""
}

func closureTokenSet(value string) map[string]bool {
	stop := map[string]bool{
		"ACUERDO": true, "NOTAS": true, "NOTA": true, "INCIDENTE": true, "EMPLEA": true, "CUANDO": true, "LUGAR": true,
		"EMERGENCIA": true, "REPORTAN": true, "REPORTANTE": true, "PARTE": true, "PERSONA": true, "UNIDAD": true, "CORPORACION": true,
		"REALIZA": true, "REALIZAN": true, "AFECTADA": true, "AFECTADO": true, "POSTERIOR": true, "GENERALMENTE": true, "ACUDE": true,
		"LLEGA": true, "MISMO": true, "MISMA": true, "ALGUNA": true, "ALGUN": true, "SOBRE": true, "ENTRE": true, "TODAS": true,
	}
	set := map[string]bool{}
	for _, token := range strings.Fields(normalizeClosureText(value)) {
		if len([]rune(token)) < 5 || stop[token] {
			continue
		}
		set[token] = true
	}
	return set
}

func normalizeClosureText(value string) string {
	value = html.UnescapeString(value)
	value = strings.ToUpper(value)
	replacer := strings.NewReplacer(
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N",
		"À", "A", "È", "E", "Ì", "I", "Ò", "O", "Ù", "U", "Ç", "C",
	)
	value = replacer.Replace(value)
	var out strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			out.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(out.String())
}

func tailRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[len(runes)-max:])
}

func containsAnyNormalized(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		needle := normalizeClosureText(phrase)
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

type closureSemanticRule struct {
	Key    string
	Weight float64
	Terms  []string
}

// closureSemanticRules convierte distintas formas de redactar una misma acción en conceptos
// operativos. Las reglas NO representan códigos de cierre: los códigos continúan definidos por
// closure_codes.json y, sobre todo, por sus definiciones oficiales. Este vocabulario únicamente
// permite comparar una nota redactada con sinónimos (carro/auto/vehículo, detenido/asegurado,
// corralón/encierro/pensión, etc.) contra el significado de cada definición.
var closureSemanticRules = []closureSemanticRule{
	{Key: "DETENCION_PERSONA", Weight: 16, Terms: []string{"PERSONA DETENIDA", "PERSONA DETENIDO", "PERSONA ASEGURADA", "PERSONA ASEGURADO", "FUE DETENIDA", "FUE DETENIDO", "FUE ASEGURADA", "FUE ASEGURADO", "SE ASEGURO A LA PERSONA", "SE DETUVO A LA PERSONA", "ARRESTADO", "ARRESTADA", "APREHENDIDO", "APREHENDIDA", "CAPTURADO", "CAPTURADA"}},
	{Key: "FLAGRANCIA_SITIO", Weight: 13, Terms: []string{"FLAGRANCIA", "EN EL MOMENTO DE LOS HECHOS", "EN EL MOMENTO QUE SUCEDE", "EN EL LUGAR DE LOS HECHOS", "DETENIDO EN EL LUGAR", "DETENIDA EN EL LUGAR", "ASEGURADO EN EL LUGAR", "ASEGURADA EN EL LUGAR"}},
	{Key: "FUGA_PERSONA", Weight: 16, Terms: []string{"SE DIO A LA FUGA", "SE DA A LA FUGA", "HUYO DEL LUGAR", "HUYO", "ESCAPO", "EMPRENDIO LA HUIDA", "LOGRO HUIR", "SE RETIRO HUYENDO", "RESPONSABLE FUGADO", "RESPONSABLE FUGADA", "MOMENTO DE LA FUGA", "DURANTE LA FUGA", "EN LA FUGA", "DURANTE LA HUIDA", "MIENTRAS HUIA", "MIENTRAS ESCAPABA"}},
	{Key: "DISPOSICION", Weight: 14, Terms: []string{"PUESTA A DISPOSICION", "PUESTO A DISPOSICION", "PONER A DISPOSICION", "PARA SER PUESTA A DISPOSICION", "PARA SER PUESTO A DISPOSICION", "QUEDO A DISPOSICION", "QUEDA A DISPOSICION", "FUE PRESENTADO ANTE", "FUE PRESENTADA ANTE", "QUEDAR A DISPOSICION"}},
	{Key: "DESTINO_MP", Weight: 17, Terms: []string{"MINISTERIO PUBLICO", "M P", "FISCALIA", "AGENCIA DEL MINISTERIO PUBLICO", "FISCAL DEL MINISTERIO PUBLICO"}},
	{Key: "DESTINO_PM", Weight: 17, Terms: []string{"POLICIA MUNICIPAL", "P M", "SEGURIDAD PUBLICA MUNICIPAL", "COMANDANCIA MUNICIPAL", "BARANDILLA", "JUEZ CALIFICADOR", "JUEZ MUNICIPAL", "AREA CORRESPONDIENTE", "AREA ADMINISTRATIVA"}},
	{Key: "VEHICULO", Weight: 11, Terms: []string{"VEHICULO", "VEHICULOS", "CARRO", "CARROS", "AUTO", "AUTOS", "AUTOMOVIL", "AUTOMOVILES", "CAMIONETA", "CAMIONETAS", "CAMION", "CAMIONES", "MOTOCICLETA", "MOTOCICLETAS", "MOTO", "MOTOS", "AUTOMOTOR"}},
	{Key: "REMISION_VEHICULO", Weight: 16, Terms: []string{"VEHICULO REMITIDO", "VEHICULO REMITIDA", "REMISION DEL VEHICULO", "REMISION DE VEHICULO", "REMITIDO AL CORRALON", "REMITIDA AL CORRALON", "LLEVADO AL CORRALON", "LLEVADA AL CORRALON", "TRASLADADO AL CORRALON", "TRASLADADA AL CORRALON", "INGRESADO AL CORRALON", "INGRESADA AL CORRALON", "ENVIADO AL CORRALON", "ENVIADA AL CORRALON", "DEPOSITADO EN EL CORRALON", "DEPOSITADA EN EL CORRALON", "LLEVADO AL ENCIERRO", "LLEVADA AL ENCIERRO", "INGRESADO A LA PENSION", "INGRESADA A LA PENSION", "DEPOSITADO EN LA PENSION", "DEPOSITADA EN LA PENSION", "VEHICULO ASEGURADO Y TRASLADADO", "VEHICULO ASEGURADO Y REMITIDO"}},
	{Key: "DESTINO_CORRALON", Weight: 17, Terms: []string{"CORRALON", "PENSION VEHICULAR", "PENSION OFICIAL", "ENCIERRO VEHICULAR", "ENCIERRO OFICIAL", "DEPOSITO VEHICULAR", "DEPOSITO OFICIAL"}},
	{Key: "ACCIDENTE_TRANSITO", Weight: 9, Terms: []string{"ACCIDENTE", "PERCANCE", "CHOQUE", "COLISION", "HECHO DE TRANSITO", "FALTA DE TRANSITO", "HECHO VIAL"}},
	{Key: "INFRACCION", Weight: 14, Terms: []string{"INFRACCION", "TARJETA DE INFRACCION", "BOLETA DE INFRACCION", "MULTA DE TRANSITO"}},
	{Key: "CONVENIO", Weight: 13, Terms: []string{"CONVENIO", "LLEGARON A UN ACUERDO", "LLEGARON A UN ARREGLO", "ACUERDO ENTRE LAS PARTES"}},
	{Key: "ARREGLO_PARTICULAR", Weight: 15, Terms: []string{"ARREGLO ENTRE PARTICULARES", "ARREGLO POR SU CUENTA", "SE ARREGLARON ENTRE ELLOS", "NO INTERVINO TRANSITO", "SIN INTERVENCION DE TRANSITO"}},
	{Key: "ASEGURADORA", Weight: 14, Terms: []string{"ASEGURADORA", "ASEGURADORAS", "AJUSTADOR", "AJUSTADORES", "SEGURO DEL VEHICULO", "COMPAÑIA DE SEGUROS"}},
	{Key: "DANOS", Weight: 6, Terms: []string{"DANOS", "DAÑOS", "AFECTACIONES MATERIALES", "DANOS MATERIALES"}},
	{Key: "RESPONSABILIDAD_DANOS", Weight: 13, Terms: []string{"SE RESPONSABILIZA DE SUS DANOS", "SE RESPONSABILIZAN DE SUS DANOS", "SE HIZO RESPONSABLE DE SUS DANOS", "CADA QUIEN PAGA SUS DANOS", "CADA PARTE ASUME SUS DANOS"}},
	{Key: "ATENCION_MEDICA", Weight: 12, Terms: []string{"ATENCION MEDICA", "ATENCION PREHOSPITALARIA", "PRIMEROS AUXILIOS", "PARAMEDICO", "PARAMEDICA", "UNIDAD MEDICA"}},
	{Key: "VALORACION_MEDICA", Weight: 13, Terms: []string{"VALORACION", "VALORADO", "VALORADA", "SIGNOS VITALES", "GLASGOW", "SE ESTABILIZA", "SE ESTABILIZO"}},
	{Key: "TRASLADO_PERSONA", Weight: 11, Terms: []string{"TRASLADO", "TRASLADADO", "TRASLADADA", "SE TRASLADA", "FUE LLEVADO AL HOSPITAL", "FUE LLEVADA AL HOSPITAL"}},
	{Key: "DESTINO_SALUD", Weight: 15, Terms: []string{"INSTITUCION DE SALUD", "HOSPITAL", "CLINICA", "CENTRO DE SALUD", "URGENCIAS", "IMSS", "ISSSTE", "ISSTECH", "HGR", "HGZ"}},
	{Key: "MUERTE", Weight: 17, Terms: []string{"FALLECIO", "FALLECE", "PERDIO LA VIDA", "SIN SIGNOS VITALES", "DECESO", "OCCISO", "OCCISA"}},
	{Key: "NEGATIVA_ATENCION", Weight: 16, Terms: []string{"SE NEGO A LA ATENCION", "SE NIEGA A LA ATENCION", "SE NEGO A SER VALORADO", "SE NEGO A SER VALORADA", "RECHAZO LA ATENCION", "NO ACEPTO SER VALORADO", "NO ACEPTO SER VALORADA"}},
	{Key: "TRASLADO_PARTICULAR", Weight: 15, Terms: []string{"POR SUS PROPIOS MEDIOS", "FAMILIARES LO TRASLADARON", "FAMILIARES LA TRASLADARON", "VEHICULO PARTICULAR", "TRASLADO POR PARTICULAR"}},
	{Key: "SEMEFO", Weight: 17, Terms: []string{"SEMEFO", "SERVICIO MEDICO FORENSE"}},
	{Key: "ALBERGUE", Weight: 15, Terms: []string{"ALBERGUE", "REFUGIO TEMPORAL"}},
	{Key: "INCENDIO", Weight: 10, Terms: []string{"INCENDIO", "FUEGO", "LLAMAS"}},
	{Key: "EXTINCION", Weight: 15, Terms: []string{"EXTINGUIDO", "EXTINGUIO", "SOFOCADO", "SOFOCO", "APAGADO", "APAGO EL FUEGO", "CONTROLADO EL FUEGO"}},
	{Key: "PASTIZAL", Weight: 13, Terms: []string{"PASTIZAL", "PASTO SECO", "TERRENO BALDIO"}},
	{Key: "QUEMA_BASURA", Weight: 15, Terms: []string{"QUEMA DE BASURA", "BASURA QUEMANDOSE", "BASURA INCENDIADA"}},
	{Key: "FUGA_SUSTANCIA", Weight: 14, Terms: []string{"FUGA DE GAS", "FUGA DE COMBUSTIBLE", "DERRAME DE GAS", "DERRAME DE SUSTANCIA", "DERRAME DE COMBUSTIBLE"}},
	{Key: "RESCATE", Weight: 15, Terms: []string{"RESCATE", "RESCATADO", "RESCATADA", "EXTRAIDO", "EXTRAIDA", "LIBERADO DE ENTRE", "LIBERADA DE ENTRE"}},
	{Key: "EVACUACION", Weight: 15, Terms: []string{"EVACUACION", "EVACUADO", "EVACUADA", "DESALOJO PREVENTIVO"}},
	{Key: "CANCELACION", Weight: 14, Terms: []string{"CANCELO EL APOYO", "CANCELAR EL APOYO", "YA NO REQUIERE EL APOYO", "DESISTE DEL APOYO"}},
	{Key: "FALTA_UNIDADES", Weight: 16, Terms: []string{"FALTA DE UNIDADES", "NO HAY UNIDADES DISPONIBLES", "SIN UNIDADES DISPONIBLES"}},
	{Key: "FALTA_COMBUSTIBLE", Weight: 16, Terms: []string{"FALTA DE COMBUSTIBLE", "SIN GASOLINA", "NO TIENE COMBUSTIBLE"}},
	{Key: "DOMICILIO_INCORRECTO", Weight: 16, Terms: []string{"DOMICILIO NO EXISTE", "DIRECCION INCORRECTA", "UBICACION INCORRECTA", "DIRECCION INCOMPLETA", "NO SE LOCALIZO EL DOMICILIO"}},
	{Key: "NO_CONTACTO", Weight: 14, Terms: []string{"NO SE LOCALIZO AL REPORTANTE", "NO FUE POSIBLE CONTACTAR", "REPORTANTE NO SALIO", "NO SE HIZO CONTACTO CON EL INCIDENTE"}},
	{Key: "INFORMA_CORPORACION", Weight: 12, Terms: []string{"SE INFORMO A LA CORPORACION", "SE NOTIFICO A LA CORPORACION", "PARA SU CONOCIMIENTO", "SE HIZO DEL CONOCIMIENTO"}},
	{Key: "CANALIZACION", Weight: 13, Terms: []string{"CANALIZADO A INSTANCIA COMPETENTE", "CANALIZADA A INSTANCIA COMPETENTE", "SE TURNO A LA INSTANCIA COMPETENTE", "SE DERIVO A LA INSTANCIA COMPETENTE"}},
	{Key: "APOYO_PREVENTIVO", Weight: 13, Terms: []string{"APOYO PREVENTIVO", "PRESENCIA PREVENTIVA", "VIGILANCIA PREVENTIVA", "RECORRIDO PREVENTIVO", "RESGUARDO PREVENTIVO"}},
	{Key: "APOYO_CIUDADANIA", Weight: 11, Terms: []string{"APOYO A LA CIUDADANIA", "AUXILIO A LA CIUDADANIA", "MEDIACION", "MEDIADOR"}},
	{Key: "ORIENTACION", Weight: 11, Terms: []string{"SE BRINDO ORIENTACION", "SE LE ORIENTO", "ORIENTACION E INFORMACION", "SOLO SE DIERON INDICACIONES"}},
	{Key: "PERSONA_LOCALIZADA", Weight: 15, Terms: []string{"PERSONA LOCALIZADA", "YA FUE LOCALIZADA LA PERSONA", "YA FUE LOCALIZADO EL MASCULINO", "YA FUE LOCALIZADA LA FEMENINA", "PERSONA YA LOCALIZADA"}},
	{Key: "VEHICULO_RECUPERADO", Weight: 15, Terms: []string{"VEHICULO RECUPERADO", "SE RECUPERO EL VEHICULO", "VEHICULO ROBADO LOCALIZADO", "CARRO ROBADO LOCALIZADO", "AUTO ROBADO LOCALIZADO"}},
	{Key: "SIN_INDICIO_DELICTIVO", Weight: 16, Terms: []string{"NO ENCONTRO INDICIO DELICTIVO", "SIN INDICIOS DELICTIVOS", "NO SE CORROBORO EL HECHO DELICTIVO", "NO SE LOCALIZARON INDICIOS DELICTIVOS"}},
	{Key: "SIN_INDICIO_EMERGENCIA", Weight: 15, Terms: []string{"NO HAY INDICIOS SOBRE LA EMERGENCIA", "NO SE ENCONTRARON INDICIOS DE LO REPORTADO", "SIN INDICIOS DE LA EMERGENCIA REPORTADA"}},
	{Key: "INTELIGENCIA", Weight: 14, Terms: []string{"LABOR DE INTELIGENCIA", "TRABAJOS DE INTELIGENCIA", "INVESTIGACION DE INTELIGENCIA"}},
	{Key: "AMONESTACION", Weight: 13, Terms: []string{"AMONESTACION VERBAL", "LLAMADO DE ATENCION", "SE LE EXHORTO"}},
	{Key: "ACORDONAMIENTO", Weight: 13, Terms: []string{"ACORDONAMIENTO", "SE ACORDONO EL AREA", "PERIMETRO DE SEGURIDAD"}},
	{Key: "COORDINACION_CORPORACIONES", Weight: 11, Terms: []string{"APOYO ENTRE CORPORACIONES", "COORDINACION ENTRE CORPORACIONES", "TRABAJARON EN CONJUNTO", "TRABAJO COORDINADO"}},
	{Key: "CONSIGNA", Weight: 13, Terms: []string{"CONSIGNA", "QUEDA EN PANTALLA PARA MONITOREO", "SE MANTIENE MONITOREO"}},
	{Key: "ORDEN_APREHENSION", Weight: 16, Terms: []string{"ORDEN DE APREHENSION", "MANDAMIENTO DE APREHENSION"}},
	{Key: "ORDEN_CATEO", Weight: 16, Terms: []string{"ORDEN DE CATEO", "DILIGENCIA DE CATEO"}},
	{Key: "ALARMA_PRIVADA", Weight: 13, Terms: []string{"ALARMA ACTIVADA", "SEGURIDAD PRIVADA", "ALARMA DE NEGOCIO", "ALARMA DE DOMICILIO"}},
	{Key: "BANCO", Weight: 9, Terms: []string{"BANCO", "BANCARIA", "SUCURSAL BANCARIA"}},
	{Key: "SEPROBAN", Weight: 16, Terms: []string{"SEPROBAN"}},
	{Key: "GRABACION", Weight: 10, Terms: []string{"GRABACION", "MENSAJE GRABADO", "GRABADORA", "CONMUTADOR"}},
	{Key: "LLAMADA", Weight: 6, Terms: []string{"LLAMADA", "OPERADOR TELEFONICO"}},
	{Key: "APLICATIVO", Weight: 12, Terms: []string{"APLICATIVO", "APLICACION"}},
}

// closureSemanticExtraRules amplía el vocabulario operativo para TODAS las definiciones del
// catálogo de cierre. No son frases obligatorias: son equivalencias de significado usadas por
// el motor para reconocer distintas formas de redactar el mismo resultado operativo.
var closureSemanticExtraRules = []closureSemanticRule{
	{Key: "DETENCION_PERSONA", Weight: 16, Terms: []string{"QUEDO DETENIDO", "QUEDO DETENIDA", "QUEDO ASEGURADO", "QUEDO ASEGURADA", "SE PROCEDIO A SU DETENCION", "SE PROCEDIO A SU ASEGURAMIENTO", "PRIVADO DE SU LIBERTAD", "BAJO CUSTODIA", "FUE INTERCEPTADO", "FUE INTERCEPTADA"}},
	{Key: "FUGA_PERSONA", Weight: 16, Terms: []string{"SE ALEJO CORRIENDO", "SE RETIRO DEL LUGAR ANTES DEL ARRIBO", "ABANDONO EL LUGAR", "SE ESCAPO", "NO FUE ALCANZADO", "NO FUE ALCANZADA"}},
	{Key: "DISPOSICION", Weight: 14, Terms: []string{"FUE ENTREGADO A", "FUE ENTREGADA A", "SE ENTREGO A LA AUTORIDAD", "QUEDO BAJO CUSTODIA DE", "SE PRESENTO ANTE LA AUTORIDAD", "QUEDAR BAJO CUSTODIA", "QUEDAR BAJO CUSTODIA DE", "QUEDA BAJO CUSTODIA", "ENTREGADO AL JUEZ", "ENTREGADA AL JUEZ"}},
	{Key: "DESTINO_MP", Weight: 17, Terms: []string{"FISCALIA GENERAL", "FISCALIA DE DISTRITO", "FISCALIA DEL ESTADO", "MINISTERIO PUBLICO DEL FUERO COMUN", "MP DEL FUERO COMUN"}},
	{Key: "DESTINO_PM", Weight: 17, Terms: []string{"DIRECCION DE SEGURIDAD PUBLICA", "SECRETARIA DE SEGURIDAD PUBLICA MUNICIPAL", "CELDAS PREVENTIVAS", "AREA DE FALTAS ADMINISTRATIVAS", "JUZGADO MUNICIPAL"}},
	{Key: "VEHICULO", Weight: 11, Terms: []string{"SEDAN", "PICKUP", "PICK UP", "TAXI", "URVAN", "VAGONETA", "TRAILER", "TRACTOCAMION", "CUATRIMOTO", "MOTONETA"}},
	{Key: "REMISION_VEHICULO", Weight: 16, Terms: []string{"FUE REMOLCADO", "FUE REMOLCADA", "SE SOLICITO GRUA", "SE LO LLEVO LA GRUA", "SE LA LLEVO LA GRUA", "QUEDO DEPOSITADO", "QUEDO DEPOSITADA", "QUEDO EN EL CORRALON", "QUEDO EN LA PENSION", "FUE PUESTO A DISPOSICION CON GRUA", "FUE TRASLADADO AL DEPOSITO", "FUE TRASLADADA AL DEPOSITO"}},
	{Key: "DESTINO_CORRALON", Weight: 17, Terms: []string{"PENSION", "ENCIERRO", "DEPOSITO", "PATIO DE RETENCION", "DEPOSITO DE VEHICULOS", "CORRALON MUNICIPAL"}},
	{Key: "ACCIDENTE_TRANSITO", Weight: 9, Terms: []string{"PERCANCE VIAL", "SINIESTRO VIAL", "HECHO VIAL", "ACCIDENTE VIAL", "CHOQUE VEHICULAR", "COLISION VEHICULAR"}},
	{Key: "INFRACCION", Weight: 14, Terms: []string{"LEVANTO INFRACCION", "LEVANTO BOLETA", "SE LE INFRACCIONO", "FOLIO DE INFRACCION", "CEDULA DE INFRACCION"}},
	{Key: "CONVENIO", Weight: 13, Terms: []string{"LLEGARON A CONCILIACION", "SE CONCILIARON", "ACORDARON ENTRE LAS PARTES", "FIRMARON CONVENIO"}},
	{Key: "ARREGLO_PARTICULAR", Weight: 15, Terms: []string{"SE ARREGLARON POR SU CUENTA", "SE RETIRARON POR ACUERDO PROPIO", "SIN INTERVENCION DEL AGENTE", "NO REQUIRIERON INTERVENCION DE TRANSITO"}},
	{Key: "ASEGURADORA", Weight: 14, Terms: []string{"ASEGURANZA", "AJUSTE DE SEGURO", "AJUSTADOR DE SEGUROS", "POLIZA DE SEGURO"}},
	{Key: "RESPONSABILIDAD_DANOS", Weight: 13, Terms: []string{"CADA CONDUCTOR SE HACE RESPONSABLE", "CADA CONDUCTOR ASUME SUS DANOS", "CADA PARTE CUBRIRA SUS DANOS", "SE RESPONSABILIZARON POR SUS DANOS", "CADA CONDUCTOR ASUMIRA SUS DANOS", "CADA CONDUCTOR CUBRIRA SUS DANOS", "CADA UNO ASUMIRA SUS PROPIOS DANOS", "CADA CONDUCTOR ASUMIRA SUS PROPIOS DANOS", "CADA CONDUCTOR PAGARA SUS PROPIOS DANOS"}},

	{Key: "UNIDAD_ARRIBO", Weight: 9, Terms: []string{"LA UNIDAD LLEGO", "LA UNIDAD ARRIBO", "AL ARRIBAR LA UNIDAD", "AL LLEGAR LA UNIDAD", "SE CONSTITUYO LA UNIDAD", "SE PRESENTO LA UNIDAD", "ACUDIO LA UNIDAD", "AL ARRIBO"}},
	{Key: "UNIDAD_NO_ARRIBO", Weight: 15, Terms: []string{"LA UNIDAD NO LLEGO", "LA UNIDAD NO ARRIBO", "NO ACUDIO LA UNIDAD", "NO FUE POSIBLE EL ARRIBO", "NO SE PRESENTO LA UNIDAD"}},
	{Key: "SIN_COMUNICACION_UNIDAD", Weight: 10, Terms: []string{"SIN COMUNICACION CON LA UNIDAD", "NO SE TUVO COMUNICACION CON LA CORPORACION", "NO CONTESTA LA UNIDAD", "SIN RESPUESTA DE LA CORPORACION"}},
	{Key: "VERIFICACION", Weight: 10, Terms: []string{"VERIFICO", "SE VERIFICO", "REALIZO VERIFICACION", "SE REALIZO RECORRIDO DE VERIFICACION", "INSPECCIONO EL LUGAR", "SE CORROBORO EN EL LUGAR"}},
	{Key: "NO_LOCALIZA_REPORTANTE", Weight: 16, Terms: []string{"NO LOCALIZO AL REPORTANTE", "NO SE LOCALIZO AL REPORTANTE", "NO ENCONTRO AL REPORTANTE", "REPORTANTE NO SALIO", "REPORTANTE NO SE PRESENTO", "NO HUBO CONTACTO CON EL REPORTANTE"}},
	{Key: "NO_LOCALIZA_OFENDIDA", Weight: 16, Terms: []string{"NO LOCALIZO A LA PERSONA OFENDIDA", "NO LOCALIZO A LA PARTE AFECTADA", "NO SE LOCALIZO A LA VICTIMA", "NO SE ENCONTRO A LA PERSONA AFECTADA", "NO UBICO A LA PARTE AFECTADA", "NO UBICO A LA PERSONA OFENDIDA"}},
	{Key: "REPORTANTE", Weight: 6, Terms: []string{"REPORTANTE", "QUIEN REPORTA", "PERSONA REPORTANTE", "CIUDADANO REPORTANTE"}},
	{Key: "OFENDIDA", Weight: 7, Terms: []string{"PERSONA OFENDIDA", "PARTE AFECTADA", "VICTIMA", "AGRAVIADO", "AGRAVIADA"}},
	{Key: "SIN_INDICIO_DELICTIVO", Weight: 17, Terms: []string{"NO HUBO INDICIOS DE DELITO", "NO SE OBSERVARON INDICIOS DELICTIVOS", "NO SE ENCONTRO EVIDENCIA DELICTIVA", "NO SE CORROBORO DELITO", "NO SE ENCONTRO NADA RELACIONADO CON EL DELITO REPORTADO", "NO ENCONTRO EVIDENCIA DELICTIVA", "NO SE ENCONTRO EVIDENCIA DELICTIVA", "SIN EVIDENCIA DELICTIVA"}},
	{Key: "SIN_INDICIO_EMERGENCIA", Weight: 16, Terms: []string{"NO SE CORROBORO LA EMERGENCIA", "NO SE ENCONTRO LA EMERGENCIA REPORTADA", "NO EXISTIA LA SITUACION REPORTADA", "NO SE OBSERVO LO REPORTADO", "SIN NOVEDAD RESPECTO A LO REPORTADO"}},

	{Key: "ATENCION_MEDICA", Weight: 12, Terms: []string{"FUE ATENDIDO", "FUE ATENDIDA", "SE BRINDO ATENCION", "SE LE BRINDO ATENCION", "SE REALIZARON PRIMEROS AUXILIOS", "SE DIO ATENCION PREHOSPITALARIA"}},
	{Key: "VALORACION_MEDICA", Weight: 13, Terms: []string{"SE VALORO", "FUE VALORADO", "FUE VALORADA", "SE REALIZO VALORACION", "TOMA DE SIGNOS VITALES", "SE TOMARON SIGNOS VITALES", "EXPLORACION FISICA"}},
	{Key: "EN_SITIO", Weight: 11, Terms: []string{"EN EL LUGAR", "EN EL SITIO", "EN EL LUGAR DE LA EMERGENCIA", "EN EL LUGAR DE LOS HECHOS", "EN LA ESCENA"}},
	{Key: "NO_TRASLADO", Weight: 16, Terms: []string{"NO AMERITA TRASLADO", "NO REQUIERE TRASLADO", "NO SE REALIZA TRASLADO", "NO FUE TRASLADADO", "NO FUE TRASLADADA", "SE QUEDA EN EL LUGAR", "SE ESTABILIZA EN EL LUGAR", "PERMANECE EN EL LUGAR"}},
	{Key: "TRASLADO_PERSONA", Weight: 11, Terms: []string{"SE REALIZA TRASLADO", "SE REALIZO TRASLADO", "FUE TRASLADADO", "FUE TRASLADADA", "SE LLEVO AL PACIENTE", "SE LLEVO A LA PACIENTE", "SE CANALIZA AL HOSPITAL"}},
	{Key: "DESTINO_SALUD", Weight: 15, Terms: []string{"NOSOCOMIO", "SANATORIO", "HOSPITAL GENERAL", "HOSPITAL REGIONAL", "UNIDAD MEDICA FAMILIAR", "UMF", "CENTRO MEDICO", "CLINICA HOSPITAL"}},
	{Key: "ATENDIDO_EN_SALUD", Weight: 16, Terms: []string{"FUE ATENDIDO EN EL HOSPITAL", "FUE ATENDIDA EN EL HOSPITAL", "RECIBIO ATENCION EN EL HOSPITAL", "FUE VALORADO EN LA CLINICA", "FUE VALORADA EN LA CLINICA", "INGRESO A URGENCIAS"}},
	{Key: "DURANTE_TRASLADO", Weight: 17, Terms: []string{"DURANTE EL TRASLADO", "EN EL TRAYECTO AL HOSPITAL", "EN CAMINO AL HOSPITAL", "MIENTRAS ERA TRASLADADO", "MIENTRAS ERA TRASLADADA"}},
	{Key: "MUERTE_EN_SALUD", Weight: 18, Terms: []string{"FALLECIO EN EL HOSPITAL", "FALLECIO EN LA CLINICA", "PERDIO LA VIDA EN EL HOSPITAL", "FALLECIO DENTRO DE LA INSTITUCION DE SALUD", "DECESO EN EL HOSPITAL", "FALLECIO DENTRO DEL HOSPITAL", "FALLECIO DENTRO DEL HOSPITAL GENERAL", "PERDIO LA VIDA DENTRO DEL HOSPITAL"}},
	{Key: "MUERTE_EN_SITIO", Weight: 18, Terms: []string{"FALLECIO EN EL LUGAR", "FALLECIO EN EL SITIO", "PERDIO LA VIDA EN EL LUGAR", "MUERTO EN SITIO", "SIN SIGNOS VITALES EN EL LUGAR"}},
	{Key: "NEGATIVA_ATENCION", Weight: 16, Terms: []string{"NO QUISO SER ATENDIDO", "NO QUISO SER ATENDIDA", "NO PERMITIO VALORACION", "REHUSA ATENCION MEDICA", "RECHAZA SER VALORADO", "RECHAZA SER VALORADA"}},
	{Key: "TRASLADO_PARTICULAR", Weight: 15, Terms: []string{"LO LLEVARAN POR SU CUENTA", "LA LLEVARAN POR SU CUENTA", "SE TRASLADA EN VEHICULO PARTICULAR", "SE RETIRA POR SUS PROPIOS MEDIOS", "FAMILIAR REALIZA EL TRASLADO"}},
	{Key: "SEMEFO", Weight: 17, Terms: []string{"MEDICINA FORENSE", "SERVICIO FORENSE", "ANFITEATRO", "SEMEFO REGIONAL"}},
	{Key: "ALBERGUE", Weight: 15, Terms: []string{"REFUGIO", "ALBERGUE TEMPORAL", "CENTRO DE RESGUARDO", "INSTANCIA DE PROTECCION"}},
	{Key: "ASESORIA_MEDICA", Weight: 15, Terms: []string{"ASESORIA MEDICA", "ORIENTACION MEDICA TELEFONICA", "INDICACIONES DE PRIMEROS AUXILIOS POR TELEFONO", "PRIMEROS AUXILIOS TELEFONICOS"}},
	{Key: "TELEFONICA", Weight: 9, Terms: []string{"POR TELEFONO", "VIA TELEFONICA", "A TRAVES DE LA LINEA TELEFONICA", "LLAMADA TELEFONICA"}},

	{Key: "INCENDIO", Weight: 10, Terms: []string{"CONATO DE INCENDIO", "SINIESTRO POR FUEGO", "MATERIAL EN COMBUSTION"}},
	{Key: "EXTINCION", Weight: 15, Terms: []string{"QUEDO EXTINGUIDO", "FUE EXTINGUIDO", "SE LOGRO EXTINGUIR", "SE EXTINGUIERON LAS LLAMAS", "SE APAGO", "QUEDO SOFOCADO", "FUE SOFOCADO"}},
	{Key: "CONTROLADO", Weight: 13, Terms: []string{"QUEDO CONTROLADO", "QUEDO CONTROLADA", "FUE CONTROLADO", "FUE CONTROLADA", "SE ENCUENTRA CONTROLADO", "SE ENCUENTRA CONTROLADA"}},
	{Key: "PROPIETARIO_VECINOS", Weight: 15, Terms: []string{"CONTROLADO POR EL PROPIETARIO", "APAGADO POR EL PROPIETARIO", "SOFOCADO POR EL PROPIETARIO", "CONTROLADO POR VECINOS", "APAGADO POR VECINOS", "CON AYUDA DE VECINOS", "APAGADO POR LOS VECINOS", "CONTROLADO POR LOS VECINOS", "SOFOCADO POR LOS VECINOS", "VECINOS APAGARON EL INCENDIO"}},
	{Key: "PASTIZAL", Weight: 13, Terms: []string{"MALEZA", "MATORRAL", "HIERBA SECA", "PASTURA", "LOTE BALDIO"}},
	{Key: "QUEMA_BASURA", Weight: 15, Terms: []string{"QUEMA DE DESECHOS", "RESIDUOS QUEMANDOSE", "MONTON DE BASURA EN LLAMAS", "DESECHOS INCENDIADOS"}},
	{Key: "FUGA_SUSTANCIA", Weight: 14, Terms: []string{"ESCAPE DE GAS", "ESCAPE DE COMBUSTIBLE", "DERRAME DE QUIMICO", "DERRAME DE HIDROCARBURO", "FUGA DE SUSTANCIA"}},
	{Key: "CIERRE_FUGA_SUSTANCIA", Weight: 16, Terms: []string{"SE CERRO LA FUGA", "FUGA CONTROLADA", "SE CONTUVO EL DERRAME", "DERRAME CONTROLADO", "SE ELIMINO LA FUGA", "SE DETUVO LA FUGA"}},
	{Key: "RESCATE", Weight: 15, Terms: []string{"SE PUSO A SALVO", "SE LIBERO A LA PERSONA ATRAPADA", "SE EXTRajo A LA PERSONA", "SE EXTRAJO A LA PERSONA", "SE RECUPERO AL ANIMAL", "SE PUSO A SALVO AL ANIMAL"}},
	{Key: "EVACUACION", Weight: 15, Terms: []string{"SE EVACUO EL INMUEBLE", "SE DESALOJO EL AREA", "SE RETIRARON A LAS PERSONAS DEL AREA DE RIESGO", "DESALOJO DE PERSONAS"}},
	{Key: "SIMULACRO", Weight: 16, Terms: []string{"SIMULACRO", "EJERCICIO DE SIMULACRO", "PRUEBA DE ALARMA", "EJERCICIO PREVENTIVO"}},
	{Key: "ACORDONAMIENTO", Weight: 13, Terms: []string{"CINTA DE PRECAUCION", "SE DELIMITO EL AREA", "SE CERRO EL PERIMETRO", "SE ESTABLECIO PERIMETRO"}},
	{Key: "PROTECCION_VIAL_PERSONAS", Weight: 10, Terms: []string{"PROTEGER A LOS TRANSEUNTES", "CUIDAR LA VIALIDAD", "EVITAR RIESGOS A LA POBLACION", "SEGURIDAD DE PEATONES"}},

	{Key: "CANCELACION", Weight: 14, Terms: []string{"REPORTANTE CANCELA", "REPORTANTE SOLICITA CANCELAR", "CIUDADANO CANCELA", "YA NO DESEA LA UNIDAD", "SOLICITA QUE NO ACUDA LA UNIDAD"}},
	{Key: "FALTA_UNIDADES", Weight: 16, Terms: []string{"TODAS LAS UNIDADES OCUPADAS", "NO SE CUENTA CON UNIDAD", "NO HAY PATRULLAS DISPONIBLES", "NO HAY AMBULANCIAS DISPONIBLES"}},
	{Key: "FALTA_COMBUSTIBLE", Weight: 16, Terms: []string{"SE QUEDO SIN COMBUSTIBLE", "NO CUENTA CON GASOLINA", "SIN DIESEL", "FALTA DE DIESEL"}},
	{Key: "DOMICILIO_INCORRECTO", Weight: 16, Terms: []string{"NO CORRESPONDE LA DIRECCION", "CALLE NO EXISTE", "NUMERO NO EXISTE", "REFERENCIA INCORRECTA", "UBICACION NO CORRESPONDE", "REFERENCIA PROPORCIONADA ERA INCORRECTA", "CALLE INDICADA NO EXISTE", "DIRECCION PROPORCIONADA ES INCORRECTA"}},
	{Key: "INFORMA_CORPORACION", Weight: 12, Terms: []string{"SE DIO AVISO A LA CORPORACION", "SE COMUNICO A LA CORPORACION", "CORPORACION QUEDA ENTERADA", "SE NOTIFICO PARA SEGUIMIENTO"}},
	{Key: "CANALIZACION", Weight: 13, Terms: []string{"SE CANALIZA A", "SE CANALIZO A", "SE TURNA A", "SE TURNO A", "SE REMITE EL REPORTE A", "SE DERIVO EL REPORTE A", "SE ENVIO PARA SU ATENCION A"}},
	{Key: "INSTANCIA_COMPETENTE", Weight: 10, Terms: []string{"INSTANCIA COMPETENTE", "AUTORIDAD COMPETENTE", "DEPENDENCIA COMPETENTE", "AREA COMPETENTE", "CORPORACION COMPETENTE"}},
	{Key: "APOYO_PREVENTIVO", Weight: 13, Terms: []string{"SE MANTUVO PRESENCIA", "SE BRINDO SEGURIDAD PREVENTIVA", "SE REALIZO VIGILANCIA", "SE QUEDO EN PREVENCION", "PRESENCIA POLICIAL PREVENTIVA"}},
	{Key: "APOYO_CIUDADANIA", Weight: 11, Terms: []string{"SE AUXILIO AL CIUDADANO", "SE APOYO AL CIUDADANO", "SE BRINDO AUXILIO", "SE MEDIA ENTRE LAS PARTES", "SE APOYO A LA PERSONA", "AUXILIARON AL CIUDADANO", "AUXILIARON A LA PERSONA", "BRINDARON AUXILIO AL CIUDADANO"}},
	{Key: "ORIENTACION", Weight: 11, Terms: []string{"SE BRINDARON RECOMENDACIONES", "SE DIERON INDICACIONES", "SE LE INDICO ACUDIR A", "SE LE RECOMENDO ACUDIR A", "SE LE EXPLICO EL PROCEDIMIENTO"}},
	{Key: "AMONESTACION", Weight: 13, Terms: []string{"SE LE HIZO UN LLAMADO DE ATENCION", "SE LE INVITO A RETIRARSE", "SE LE INDICO QUE CESARA", "SE LE APERCIBIO VERBALMENTE", "SE LE DIO UNA ADVERTENCIA"}},
	{Key: "COORDINACION_CORPORACIONES", Weight: 11, Terms: []string{"ACUDIERON VARIAS CORPORACIONES", "APOYO CONJUNTO", "OPERATIVO CONJUNTO", "COORDINACION INTERINSTITUCIONAL", "EN COORDINACION CON"}},
	{Key: "CONSIGNA", Weight: 13, Terms: []string{"QUEDA PENDIENTE", "SE DA SEGUIMIENTO", "PERMANECE ACTIVO", "CONTINUA EN MONITOREO", "SE MANTIENE EN PANTALLA"}},

	{Key: "PERSONA_LOCALIZADA", Weight: 15, Terms: []string{"APARECIO LA PERSONA", "REGRESO A SU DOMICILIO", "FUE UBICADA", "FUE UBICADO", "SE ENCONTRO A LA PERSONA", "YA SE ENCUENTRA CON SU FAMILIA"}},
	{Key: "PERSONA_DESAPARECIDA", Weight: 10, Terms: []string{"PERSONA DESAPARECIDA", "NO LOCALIZADA", "EXTRAVIADA", "EXTRAVIADO", "EN CALIDAD DE DESAPARECIDA", "EN CALIDAD DE DESAPARECIDO"}},
	{Key: "VEHICULO_RECUPERADO", Weight: 15, Terms: []string{"FUE RECUPERADO EL AUTO", "FUE RECUPERADA LA CAMIONETA", "SE ENCONTRO EL CARRO", "SE LOCALIZO LA UNIDAD ROBADA", "APARECIO EL VEHICULO", "VEHICULO FUE ENCONTRADO", "AUTOMOVIL FUE ENCONTRADO", "CARRO FUE ENCONTRADO", "FUE ENCONTRADO Y RECUPERADO", "FUE ENCONTRADA Y RECUPERADA", "RECUPERADO EN OTRA COLONIA"}},
	{Key: "ROBO_VEHICULO", Weight: 11, Terms: []string{"VEHICULO ROBADO", "ROBO DE VEHICULO", "REPORTE DE ROBO", "UNIDAD CON REPORTE DE ROBO", "AUTOMOVIL ROBADO", "MOTOCICLETA ROBADA"}},
	{Key: "INTELIGENCIA", Weight: 14, Terms: []string{"SE REALIZARON INVESTIGACIONES", "SE REALIZO SEGUIMIENTO", "TRABAJO DE INVESTIGACION", "SEGUIMIENTO DE ACTOS DELICTIVOS", "RECABAR INFORMACION"}},
	{Key: "ORDEN_APREHENSION", Weight: 16, Terms: []string{"MANDAMIENTO JUDICIAL DE APREHENSION", "CUMPLIMIENTO DE ORDEN DE APREHENSION", "ORDEN JUDICIAL DE APREHENSION"}},
	{Key: "ORDEN_CATEO", Weight: 16, Terms: []string{"DILIGENCIA JUDICIAL DE CATEO", "CUMPLIMIENTO DE CATEO", "MANDAMIENTO DE CATEO"}},

	{Key: "ALARMA", Weight: 9, Terms: []string{"ALARMA", "ACTIVACION DE ALARMA", "SE ACTIVO LA ALARMA", "REPORTE DE ALARMA"}},
	{Key: "ALARMA_PRIVADA", Weight: 13, Terms: []string{"SEGURIDAD PRIVADA", "EMPRESA DE SEGURIDAD", "CENTRAL PRIVADA DE ALARMAS", "ALARMA DE CASA", "ALARMA DE COMERCIO", "CASA DE EMPENO", "CENTRAL PRIVADA DE SEGURIDAD", "SERVICIO PRIVADO DE SEGURIDAD", "SEGURIDAD PRIVADA CONTRATADA", "CENTRAL DE SEGURIDAD CONTRATADA"}},
	{Key: "BANCO", Weight: 9, Terms: []string{"INSTITUCION BANCARIA", "SUCURSAL DE BANCO", "BANCO DEL BIENESTAR", "CAJERO BANCARIO"}},
	{Key: "SEPROBAN", Weight: 16, Terms: []string{"SEGURIDAD Y PROTECCION BANCARIA", "CENTRAL SEPROBAN"}},
	{Key: "GRABACION", Weight: 10, Terms: []string{"AUDIO GRABADO", "MENSAJE AUTOMATICO", "CONMUTADOR AUTOMATICO", "VOZ GRABADA"}},
	{Key: "LLAMADA", Weight: 8, Terms: []string{"LLAMADA DE OPERADOR", "OPERADOR DE CENTRAL", "SE RECIBE LLAMADA", "VIA LLAMADA"}},
	{Key: "APLICATIVO", Weight: 12, Terms: []string{"APP SEPROBAN", "PLATAFORMA SEPROBAN", "SISTEMA SEPROBAN", "ALERTA DE APLICACION"}},
	{Key: "ORDEN_SUPERIOR", Weight: 16, Terms: []string{"POR ORDEN SUPERIOR", "POR ORDENES SUPERIORES", "INSTRUCCION SUPERIOR", "POR DISPOSICION DEL MANDO", "ORDEN DEL MANDO"}},
	{Key: "UNIDADES_CONCENTRADAS", Weight: 14, Terms: []string{"UNIDADES CONCENTRADAS", "UNIDADES EN BASE", "UNIDADES EN EVENTO", "PERSONAL CONCENTRADO", "UNIDADES ACUARTELADAS", "UNIDADES PERMANECIERON EN BASE", "UNIDADES SE MANTUVIERON EN BASE", "LAS UNIDADES PERMANECIERON EN BASE"}},
}

func containsSemanticTerm(normalizedText, term string) bool {
	needle := normalizeClosureText(term)
	if needle == "" {
		return false
	}
	return strings.Contains(" "+normalizedText+" ", " "+needle+" ")
}

func semanticConceptSet(value string) (map[string]bool, map[string]string) {
	text := normalizeClosureText(value)
	set := map[string]bool{}
	evidence := map[string]string{}
	allRules := make([]closureSemanticRule, 0, len(closureSemanticRules)+len(closureSemanticExtraRules))
	allRules = append(allRules, closureSemanticRules...)
	allRules = append(allRules, closureSemanticExtraRules...)
	for _, rule := range allRules {
		for _, term := range rule.Terms {
			if containsSemanticTerm(text, term) {
				set[rule.Key] = true
				if evidence[rule.Key] == "" {
					evidence[rule.Key] = term
				}
				break
			}
		}
	}
	return set, evidence
}

func semanticHas(set map[string]bool, keys ...string) bool {
	for _, key := range keys {
		if set[key] {
			return true
		}
	}
	return false
}

func semanticRuleWeight(key string) float64 {
	best := 0.0
	for _, rule := range closureSemanticRules {
		if rule.Key == key && rule.Weight > best {
			best = rule.Weight
		}
	}
	for _, rule := range closureSemanticExtraRules {
		if rule.Key == key && rule.Weight > best {
			best = rule.Weight
		}
	}
	if best > 0 {
		return best
	}
	return 5
}

// semanticOperationalClosure resuelve relaciones que aparecen en varias definiciones y que suelen
// redactarse con sinónimos. La selección se basa en la combinación de conceptos exigida por las
// definiciones oficiales, no en que el usuario escriba una oración exacta.
func semanticOperationalClosure(profile, text string) (string, string, bool) {
	concepts, evidence := semanticConceptSet(text)
	vehicle := concepts["VEHICULO"]
	remitted := concepts["REMISION_VEHICULO"]
	vehicleDestination := concepts["DESTINO_CORRALON"] || concepts["DESTINO_MP"]
	detained := concepts["DETENCION_PERSONA"]
	fled := concepts["FUGA_PERSONA"]
	disposition := concepts["DISPOSICION"]

	// Definiciones 42/54/55: lo esencial es el resultado del vehículo y, cuando aplica,
	// el resultado de la persona responsable. "carro", "auto", "moto", etc. se unifican en VEHICULO.
	if vehicle && remitted && vehicleDestination {
		if detained {
			return "55", "POR DEFINICIÓN: SE IDENTIFICÓ PERSONA DETENIDA/ASEGURADA + VEHÍCULO (" + evidence["VEHICULO"] + ") REMITIDO A CORRALÓN/FISCALÍA", true
		}
		if fled {
			return "54", "POR DEFINICIÓN: EL RESPONSABLE HUYÓ Y EL VEHÍCULO (" + evidence["VEHICULO"] + ") FUE REMITIDO A CORRALÓN/FISCALÍA", true
		}
		if profile == "TRANSITO" || concepts["ACCIDENTE_TRANSITO"] || concepts["INFRACCION"] {
			return "42", "POR DEFINICIÓN: EL VEHÍCULO (" + evidence["VEHICULO"] + ") FUE REMITIDO A CORRALÓN/PENSIÓN/ENCIERRO", true
		}
		// Aun fuera de Tránsito, el resultado final del vehículo es suficiente para sugerir 42,
		// pero no para cierre automático si falta el contexto de accidente/falta de tránsito.
		return "42", "POR DEFINICIÓN: SE IDENTIFICÓ REMISIÓN DE UN VEHÍCULO A CORRALÓN/PENSIÓN/ENCIERRO; REVISA EL CONTEXTO DE TRÁNSITO", false
	}

	// Definiciones 5/23/49/74: se separa estrictamente P.M. de M.P. y se conserva la fuga.
	if detained && disposition {
		if concepts["DESTINO_MP"] {
			if fled {
				return "74", "POR DEFINICIÓN: PERSONA DETENIDA DURANTE/DESPUÉS DE LA FUGA Y PUESTA A DISPOSICIÓN DEL M.P.", true
			}
			safe := concepts["FLAGRANCIA_SITIO"]
			return "5", "POR DEFINICIÓN: PERSONA DETENIDA/ASEGURADA Y PUESTA A DISPOSICIÓN DEL M.P.", safe
		}
		if concepts["DESTINO_PM"] {
			if fled {
				return "49", "POR DEFINICIÓN: PERSONA DETENIDA DURANTE/DESPUÉS DE LA FUGA Y PUESTA A DISPOSICIÓN DE LA P.M.", true
			}
			safe := concepts["FLAGRANCIA_SITIO"]
			return "23", "POR DEFINICIÓN: PERSONA DETENIDA/ASEGURADA Y PUESTA A DISPOSICIÓN DE LA P.M.", safe
		}
	}
	return "", "", false
}

func semanticDefinitionScore(item ClosureCode, noteConcepts map[string]bool, noteEvidence map[string]string) (float64, []string, float64) {
	definitionConcepts, _ := semanticConceptSet(item.Name + " " + item.Definition)
	if len(definitionConcepts) == 0 {
		return 0, nil, 0
	}
	totalWeight := 0.0
	matchedWeight := 0.0
	matchedCount := 0
	evidence := []string{}
	for key := range definitionConcepts {
		weight := semanticRuleWeight(key)
		totalWeight += weight
		if noteConcepts[key] {
			matchedWeight += weight
			matchedCount++
			if len(evidence) < 5 {
				label := key
				if noteEvidence[key] != "" {
					label += ": " + noteEvidence[key]
				}
				evidence = append(evidence, label)
			}
		}
	}
	if matchedCount == 0 || totalWeight == 0 {
		return 0, nil, 0
	}
	coverage := matchedWeight / totalWeight
	// La cobertura pesa más que la mera cantidad de palabras: obliga a que la nota represente
	// varias partes del significado de la definición, no una palabra aislada.
	score := matchedWeight*1.55 + coverage*42
	if matchedCount >= 2 {
		score += float64(matchedCount-1) * 6
	}
	if coverage >= .72 && matchedCount >= 2 {
		score += 18
	}
	return score, evidence, coverage
}

// closureDefinitionScenario expresa, en conceptos, las condiciones contenidas en cada definición
// oficial. El texto de la nota NO tiene que coincidir literalmente con estos nombres: primero se
// normaliza a conceptos mediante closureSemanticRules/closureSemanticExtraRules.
type closureDefinitionScenario struct {
	Code        string
	Require     []string
	Any         [][]string
	Forbid      []string
	Bonus       []string
	Priority    float64
	AutoRequire []string
	NeverAuto   bool
}

// Los 65 códigos del documento de definiciones están representados aquí. Las condiciones reflejan
// el significado de cada definición: acción, sujeto, resultado, destino y/o causa. Los perfiles de
// corporación solo aportan contexto; nunca sustituyen lo que dice el resultado de la nota.
var closureDefinitionScenarios = []closureDefinitionScenario{
	{Code: "2", Require: []string{"SIN_INDICIO_EMERGENCIA"}, Forbid: []string{"SIN_INDICIO_DELICTIVO"}, Bonus: []string{"UNIDAD_ARRIBO", "VERIFICACION"}, Priority: 62, AutoRequire: []string{"VERIFICACION"}},
	{Code: "4", Require: []string{"APOYO_CIUDADANIA"}, Bonus: []string{"UNIDAD_ARRIBO"}, Priority: 38},
	{Code: "5", Require: []string{"DETENCION_PERSONA", "DISPOSICION", "DESTINO_MP"}, Forbid: []string{"FUGA_PERSONA"}, Bonus: []string{"FLAGRANCIA_SITIO"}, Priority: 88, AutoRequire: []string{"FLAGRANCIA_SITIO"}},
	{Code: "6", Require: []string{"AMONESTACION"}, Forbid: []string{"DETENCION_PERSONA"}, Priority: 48},
	{Code: "7", Require: []string{"APOYO_PREVENTIVO"}, Priority: 43},
	{Code: "8", Require: []string{"DOMICILIO_INCORRECTO"}, Priority: 72},
	{Code: "13", Require: []string{"INFORMA_CORPORACION"}, Forbid: []string{"CANALIZACION"}, Priority: 45, NeverAuto: true},
	{Code: "15", Require: []string{"FUGA_PERSONA"}, Forbid: []string{"DETENCION_PERSONA", "REMISION_VEHICULO", "MUERTE"}, Priority: 52},
	{Code: "16", Require: []string{"FUGA_PERSONA", "MUERTE"}, Bonus: []string{"DURANTE_TRASLADO"}, Priority: 96},
	{Code: "17", Any: [][]string{{"ATENCION_MEDICA", "VALORACION_MEDICA"}, {"EN_SITIO", "NO_TRASLADO"}}, Forbid: []string{"DESTINO_SALUD", "TRASLADO_PARTICULAR", "MUERTE"}, Bonus: []string{"NO_TRASLADO"}, Priority: 66},
	{Code: "18", Require: []string{"ATENDIDO_EN_SALUD"}, Forbid: []string{"MUERTE"}, Bonus: []string{"DESTINO_SALUD"}, Priority: 67},
	{Code: "19", Require: []string{"MUERTE"}, Any: [][]string{{"MUERTE_EN_SITIO", "EN_SITIO"}}, Forbid: []string{"DURANTE_TRASLADO", "MUERTE_EN_SALUD"}, Priority: 85},
	{Code: "20", Require: []string{"FUGA_SUSTANCIA"}, Any: [][]string{{"CIERRE_FUGA_SUSTANCIA", "CONTROLADO"}}, Priority: 82},
	{Code: "21", Require: []string{"INCENDIO"}, Any: [][]string{{"EXTINCION", "CONTROLADO"}}, Forbid: []string{"PASTIZAL", "QUEMA_BASURA", "PROPIETARIO_VECINOS"}, Priority: 70},
	{Code: "22", Require: []string{"COORDINACION_CORPORACIONES"}, Priority: 54},
	{Code: "23", Require: []string{"DETENCION_PERSONA", "DISPOSICION", "DESTINO_PM"}, Forbid: []string{"FUGA_PERSONA"}, Bonus: []string{"FLAGRANCIA_SITIO"}, Priority: 88, AutoRequire: []string{"FLAGRANCIA_SITIO"}},
	{Code: "24", Require: []string{"SIMULACRO"}, Priority: 73},
	{Code: "25", Require: []string{"NEGATIVA_ATENCION"}, Priority: 90},
	{Code: "26", Require: []string{"TRASLADO_PERSONA", "ALBERGUE"}, Forbid: []string{"NO_TRASLADO"}, Priority: 82},
	{Code: "27", Require: []string{"SEMEFO"}, Any: [][]string{{"TRASLADO_PERSONA", "MUERTE"}}, Priority: 92},
	{Code: "28", Require: []string{"UNIDAD_NO_ARRIBO"}, Forbid: []string{"FALTA_UNIDADES", "FALTA_COMBUSTIBLE", "ORDEN_SUPERIOR"}, Bonus: []string{"SIN_COMUNICACION_UNIDAD"}, Priority: 64},
	{Code: "29", Require: []string{"ORIENTACION"}, Forbid: []string{"DETENCION_PERSONA", "CANALIZACION"}, Priority: 50},
	{Code: "30", Require: []string{"RESCATE"}, Priority: 76},
	{Code: "32", Require: []string{"CANCELACION"}, Bonus: []string{"REPORTANTE"}, Priority: 78},
	{Code: "34", Require: []string{"TRASLADO_PARTICULAR"}, Forbid: []string{"DESTINO_CORRALON"}, Priority: 88},
	{Code: "35", Require: []string{"TRASLADO_PERSONA", "DESTINO_SALUD"}, Any: [][]string{{"ATENCION_MEDICA", "VALORACION_MEDICA"}}, Forbid: []string{"NO_TRASLADO", "MUERTE"}, Bonus: []string{"EN_SITIO"}, Priority: 94},
	{Code: "36", Require: []string{"FALTA_UNIDADES"}, Bonus: []string{"UNIDAD_NO_ARRIBO"}, Priority: 92},
	{Code: "37", Require: []string{"TRASLADO_PERSONA", "DESTINO_SALUD", "MUERTE_EN_SITIO"}, Priority: 100},
	{Code: "38", Require: []string{"CONSIGNA"}, Priority: 59, NeverAuto: true},
	{Code: "39", Require: []string{"CONVENIO"}, Forbid: []string{"ARREGLO_PARTICULAR", "ASEGURADORA"}, Bonus: []string{"UNIDAD_ARRIBO"}, Priority: 57},
	{Code: "40", Require: []string{"EVACUACION"}, Priority: 83},
	{Code: "41", Require: []string{"VEHICULO_RECUPERADO"}, Any: [][]string{{"ROBO_VEHICULO", "VEHICULO"}}, Priority: 86},
	{Code: "42", Require: []string{"VEHICULO", "REMISION_VEHICULO"}, Any: [][]string{{"DESTINO_CORRALON", "DESTINO_MP"}}, Forbid: []string{"FUGA_PERSONA", "DETENCION_PERSONA"}, Bonus: []string{"ACCIDENTE_TRANSITO", "INFRACCION"}, Priority: 79},
	{Code: "43", Any: [][]string{{"NO_LOCALIZA_REPORTANTE", "NO_CONTACTO"}}, Forbid: []string{"NO_LOCALIZA_OFENDIDA"}, Bonus: []string{"UNIDAD_ARRIBO", "REPORTANTE"}, Priority: 72},
	{Code: "44", Require: []string{"ASESORIA_MEDICA"}, Bonus: []string{"TELEFONICA"}, Priority: 82},
	{Code: "45", Require: []string{"ORDEN_APREHENSION"}, Priority: 96},
	{Code: "46", Require: []string{"TRASLADO_PERSONA", "DESTINO_SALUD"}, Forbid: []string{"NO_TRASLADO", "MUERTE", "VALORACION_MEDICA", "ATENCION_MEDICA"}, Priority: 69},
	{Code: "47", Require: []string{"TRASLADO_PERSONA", "MUERTE", "DURANTE_TRASLADO"}, Priority: 104},
	{Code: "48", Require: []string{"MUERTE_EN_SALUD"}, Bonus: []string{"TRASLADO_PERSONA", "DESTINO_SALUD"}, Priority: 105},
	{Code: "49", Require: []string{"DETENCION_PERSONA", "FUGA_PERSONA", "DISPOSICION", "DESTINO_PM"}, Priority: 108},
	{Code: "52", Require: []string{"CANALIZACION"}, Any: [][]string{{"INSTANCIA_COMPETENTE", "INFORMA_CORPORACION"}}, Priority: 76},
	{Code: "53", Require: []string{"ORDEN_CATEO"}, Priority: 98},
	{Code: "54", Require: []string{"FUGA_PERSONA", "VEHICULO", "REMISION_VEHICULO"}, Any: [][]string{{"DESTINO_CORRALON", "DESTINO_MP"}}, Priority: 110},
	{Code: "55", Require: []string{"DETENCION_PERSONA", "VEHICULO", "REMISION_VEHICULO"}, Any: [][]string{{"DESTINO_CORRALON", "DESTINO_MP"}}, Priority: 111},
	{Code: "56", Require: []string{"INFRACCION"}, Bonus: []string{"ACCIDENTE_TRANSITO"}, Priority: 91},
	{Code: "57", Require: []string{"RESPONSABILIDAD_DANOS"}, Bonus: []string{"ACCIDENTE_TRANSITO", "DANOS"}, Forbid: []string{"ASEGURADORA", "ARREGLO_PARTICULAR"}, Priority: 90},
	{Code: "58", Require: []string{"ARREGLO_PARTICULAR"}, Bonus: []string{"ACCIDENTE_TRANSITO", "CONVENIO"}, Priority: 94},
	{Code: "59", Require: []string{"ASEGURADORA"}, Any: [][]string{{"RESPONSABILIDAD_DANOS", "DANOS"}}, Bonus: []string{"ACCIDENTE_TRANSITO"}, Priority: 96},
	{Code: "60", Require: []string{"PERSONA_LOCALIZADA"}, Bonus: []string{"PERSONA_DESAPARECIDA", "REPORTANTE"}, Priority: 88},
	{Code: "63", Require: []string{"ALARMA", "ALARMA_PRIVADA"}, Forbid: []string{"SEPROBAN", "BANCO"}, Priority: 81},
	{Code: "64", Require: []string{"ALARMA", "BANCO", "LLAMADA"}, Forbid: []string{"SEPROBAN", "GRABACION", "APLICATIVO"}, Priority: 88},
	{Code: "65", Require: []string{"ALARMA", "BANCO", "GRABACION"}, Forbid: []string{"SEPROBAN", "APLICATIVO"}, Priority: 89},
	{Code: "66", Require: []string{"ALARMA", "BANCO", "SEPROBAN", "GRABACION"}, Forbid: []string{"APLICATIVO"}, Priority: 101},
	{Code: "67", Require: []string{"ALARMA", "BANCO", "SEPROBAN", "LLAMADA"}, Forbid: []string{"GRABACION", "APLICATIVO"}, Priority: 101},
	{Code: "68", Require: []string{"ALARMA", "BANCO", "SEPROBAN", "APLICATIVO"}, Priority: 102},
	{Code: "69", Require: []string{"UNIDAD_ARRIBO", "NO_LOCALIZA_OFENDIDA"}, Forbid: []string{"NO_LOCALIZA_REPORTANTE"}, Priority: 92},
	{Code: "70", Require: []string{"UNIDAD_ARRIBO", "VERIFICACION", "SIN_INDICIO_DELICTIVO"}, Priority: 97},
	{Code: "71", Require: []string{"UNIDAD_NO_ARRIBO", "FALTA_COMBUSTIBLE"}, Priority: 103},
	{Code: "73", Require: []string{"UNIDADES_CONCENTRADAS", "ORDEN_SUPERIOR"}, Priority: 100},
	{Code: "74", Require: []string{"DETENCION_PERSONA", "FUGA_PERSONA", "DISPOSICION", "DESTINO_MP"}, Priority: 109},
	{Code: "75", Require: []string{"INCENDIO", "PROPIETARIO_VECINOS"}, Any: [][]string{{"CONTROLADO", "EXTINCION"}}, Priority: 104},
	{Code: "76", Require: []string{"INCENDIO", "PASTIZAL"}, Any: [][]string{{"CONTROLADO", "EXTINCION"}}, Priority: 103},
	{Code: "77", Require: []string{"INCENDIO", "QUEMA_BASURA"}, Any: [][]string{{"CONTROLADO", "EXTINCION"}}, Priority: 103},
	{Code: "78", Require: []string{"ACORDONAMIENTO"}, Bonus: []string{"PROTECCION_VIAL_PERSONAS"}, Priority: 82},
	{Code: "79", Require: []string{"INTELIGENCIA"}, Priority: 84},
}

func conceptPresent(set map[string]bool, key string) bool { return set[key] }

func scenarioProfileBonus(profile, code string) float64 {
	family := closureCodeFamily(code)
	switch profile {
	case "PROTECCION_CIVIL":
		if family == "MEDICO" || family == "PROTECCION_CIVIL" {
			return 7
		}
	case "SEGURIDAD":
		if family == "SEGURIDAD" {
			return 7
		}
	case "TRANSITO":
		if family == "TRANSITO" {
			return 7
		}
	}
	return 0
}

func evaluateDefinitionScenario(sc closureDefinitionScenario, set map[string]bool, evidence map[string]string, profile string) (float64, []string, bool) {
	ev := []string{}
	score := sc.Priority + scenarioProfileBonus(profile, sc.Code)
	for _, key := range sc.Forbid {
		if conceptPresent(set, key) {
			return 0, nil, false
		}
	}
	for _, key := range sc.Require {
		if !conceptPresent(set, key) {
			return 0, nil, false
		}
		score += semanticRuleWeight(key)
		label := key
		if evidence[key] != "" {
			label += ": " + evidence[key]
		}
		ev = append(ev, label)
	}
	for _, group := range sc.Any {
		bestKey := ""
		bestWeight := -1.0
		for _, key := range group {
			if conceptPresent(set, key) && semanticRuleWeight(key) > bestWeight {
				bestKey, bestWeight = key, semanticRuleWeight(key)
			}
		}
		if bestKey == "" {
			return 0, nil, false
		}
		score += bestWeight
		label := bestKey
		if evidence[bestKey] != "" {
			label += ": " + evidence[bestKey]
		}
		ev = append(ev, label)
	}
	for _, key := range sc.Bonus {
		if conceptPresent(set, key) {
			score += semanticRuleWeight(key) * 0.45
		}
	}
	safe := !sc.NeverAuto
	for _, key := range sc.AutoRequire {
		if !conceptPresent(set, key) {
			safe = false
			break
		}
	}
	return score, ev, safe
}

// definitionDrivenClosure compara el resultado final con TODAS las definiciones del catálogo a
// través de conceptos. No bloquea por corporación: el perfil solo da un pequeño desempate. De esta
// forma una unidad policial puede tener un resultado médico si la nota realmente documenta la
// intervención médica, y una nota de PC puede registrar coordinación, canalización, etc.
func definitionDrivenClosure(profile, text string) (string, string, bool) {
	set, evidence := semanticConceptSet(text)
	type hit struct {
		code  string
		score float64
		ev    []string
		safe  bool
	}
	hits := []hit{}
	for _, sc := range closureDefinitionScenarios {
		score, ev, safe := evaluateDefinitionScenario(sc, set, evidence, profile)
		if score > 0 {
			hits = append(hits, hit{sc.Code, score, ev, safe})
		}
	}
	if len(hits) == 0 {
		return "", "", false
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].code < hits[j].code
		}
		return hits[i].score > hits[j].score
	})
	top := hits[0]
	// Si dos definiciones quedan prácticamente empatadas, no se fuerza un resultado automático.
	// Se permite sugerir la más específica, pero se exige revisión humana.
	if len(hits) > 1 && top.score-hits[1].score < 7 {
		top.safe = false
	}
	item, ok := closureByCode[top.code]
	if !ok {
		return "", "", false
	}
	evtxt := strings.Join(top.ev, " + ")
	reason := fmt.Sprintf("DEFINICIÓN %s · %s: EL RESULTADO DE LA NOTA CUMPLE LOS CONCEPTOS OPERATIVOS %s. LA CORPORACIÓN SOLO SE USA COMO CONTEXTO; LA DECISIÓN PROVIENE DE LA DEFINICIÓN.", item.Code, item.Name, evtxt)
	return top.code, reason, top.safe
}

func isProtectionCivilFieldLabel(normalized string) bool {
	labels := []string{
		"FECHA", "SOLICITA", "AMBULANCIA", "JEFE DE SERVICIO", "TIPO DE SERVICIO", "CRONOMETRIA",
		"AVISO", "LLEGADA AL LUGAR", "ATENCION", "TERMINO DE SERVICIO", "DATOS DEL PACIENTE", "NOMBRE",
		"DOMICILIO", "EDAD", "FAMILIAR", "OCUPACION", "LUGAR DE OCURRENCIA Y UBICACION DEL SERVICIO",
		"CAUSA DE LA EMERGENCIA", "ORIGEN PROBABLE", "ANAMNESIS", "VIA AEREA", "PULSO", "SIGNOS VITALES INICIALES",
		"FR", "FC", "SAT02", "SAT O2", "P A", "GLUCOSA", "ALERGIAS", "MEDICAMENTOS", "PADECIMIENTOS",
		"ULTIMO LUNCH", "EVENTO PREVIOS", "EVENTOS PREVIOS", "ESCALA DEL COMA DE GLASGOW POST TX", "PERTENENCIAS",
		"TRASLADO", "RECIBE", "ATIENDE", "OPERADOR",
	}
	for _, label := range labels {
		if normalized == label {
			return true
		}
	}
	return false
}

func protectionCivilFieldValue(lines []string, label string) string {
	labelNorm := normalizeClosureText(label)
	for i, line := range lines {
		raw := strings.TrimSpace(strings.Trim(line, "*#•- \t"))
		n := normalizeClosureText(raw)
		if n != labelNorm && !strings.HasPrefix(n, labelNorm+" ") {
			continue
		}

		// Plantilla frecuente: "*Traslado:* No Amerita / Se estabiliza en el lugar".
		if idx := strings.Index(raw, ":"); idx >= 0 {
			if value := strings.TrimSpace(raw[idx+1:]); value != "" {
				return normalizeClosureText(value)
			}
		}
		if strings.HasPrefix(n, labelNorm+" ") {
			if value := strings.TrimSpace(strings.TrimPrefix(n, labelNorm)); value != "" {
				return value
			}
		}

		// Cuando la etiqueta está sola, toma la primera línea siguiente que no sea otra etiqueta.
		for j := i + 1; j < len(lines) && j <= i+2; j++ {
			value := normalizeClosureText(lines[j])
			if value == "" {
				continue
			}
			if isProtectionCivilFieldLabel(value) {
				break
			}
			return value
		}
		return ""
	}
	return ""
}

func protectionCivilMedicalAssessment(full string) bool {
	// Los datos objetivos de valoración en la plantilla de PC demuestran atención en sitio.
	objective := containsAnyNormalized(full,
		"SIGNOS VITALES", "GLASGOW", "VIA AEREA", "PULSO RADIAL", "SAT02", "SAT O2",
		"P A", "FR ", "FC ", "ANAMNESIS")
	if !objective {
		return false
	}
	// Exige que exista al menos un dato clínico numérico para no confundir una plantilla vacía con atención real.
	numericClinical := regexp.MustCompile(`(?:GLASGOW|SAT02|SAT O2|\bFR\b|\bFC\b|\bP A\b)[^0-9]{0,12}[0-9]`)
	return numericClinical.MatchString(full) || containsAnyNormalized(full, "SE ESTABILIZA EN EL LUGAR", "SE ESTABILIZO EN EL LUGAR")
}

func structuredProtectionCivilClosure(note Note) (string, string) {
	lines := plainNoteLines(note.ContenidoHTML)
	if len(lines) == 0 {
		return "", ""
	}
	full := normalizeClosureText(strings.Join(lines, " "))

	// Identifica la estructura operacional de Protección Civil por varios campos, no solo por el nombre de la corporación.
	signals := 0
	for _, marker := range []string{"PROTECCION CIVIL", "AMBULANCIA", "JEFE DE SERVICIO", "CRONOMETRIA", "DATOS DEL PACIENTE", "SIGNOS VITALES", "TRASLADO"} {
		if strings.Contains(full, marker) {
			signals++
		}
	}
	if signals < 4 || !strings.Contains(full, "TRASLADO") {
		return "", ""
	}

	// Los desenlaces de muerte se dejan al analizador general, que distingue sitio, traslado e institución de salud.
	if containsAnyNormalized(full, "FALLECIO", "FALLECE", "SIN SIGNOS VITALES", "MUERTO EN SITIO", "DECESO") {
		return "", ""
	}

	traslado := protectionCivilFieldValue(lines, "TRASLADO")
	recibe := protectionCivilFieldValue(lines, "RECIBE")
	assessment := protectionCivilMedicalAssessment(full)

	// Negaciones del traslado escritas como las usan las tarjetas de PC.
	noTransfer := containsAnyNormalized(traslado,
		"NO AMERITA", "NO AMERITO", "NO REQUIERE", "NO REQUIRIO", "NO APLICA", "SIN TRASLADO",
		"NO SE REALIZA", "NO SE REALIZO", "SE ESTABILIZA EN EL LUGAR", "SE ESTABILIZO EN EL LUGAR")
	if noTransfer {
		if assessment || containsAnyNormalized(traslado, "SE ESTABILIZA EN EL LUGAR", "SE ESTABILIZO EN EL LUGAR") {
			return "17", "PROTECCIÓN CIVIL: EL CAMPO TRASLADO INDICA '" + traslado + "' Y LA NOTA CONTIENE VALORACIÓN/ESTABILIZACIÓN EN SITIO. CORRESPONDE ATENCIÓN MÉDICA EN SITIO SIN TRASLADO"
		}
		return "", ""
	}

	// Negativa a la atención solo aplica si la persona se niega a ser valorada o atendida; no por una simple negativa de traslado.
	if containsAnyNormalized(full,
		"SE NEGO A LA ATENCION", "SE NIEGA A LA ATENCION", "SE NIEGA A SER VALORADA", "SE NIEGA A SER VALORADO",
		"SE NIEGA A SER ATENDIDA", "SE NIEGA A SER ATENDIDO", "RECHAZO LA ATENCION", "NO ACEPTO SER VALORADA", "NO ACEPTO SER VALORADO") {
		return "25", "PROTECCIÓN CIVIL: LA NARRATIVA INDICA NEGATIVA EXPRESA A LA VALORACIÓN O ATENCIÓN MÉDICA"
	}

	if containsAnyNormalized(traslado, "POR SUS PROPIOS MEDIOS", "PARTICULAR", "FAMILIAR", "VEHICULO PARTICULAR") {
		return "34", "PROTECCIÓN CIVIL: EL CAMPO TRASLADO INDICA QUE LA PERSONA FUE TRASLADADA POR PARTICULAR O POR SUS PROPIOS MEDIOS"
	}
	if containsAnyNormalized(traslado, "SEMEFO", "SERVICIO MEDICO FORENSE") {
		return "27", "PROTECCIÓN CIVIL: EL CAMPO TRASLADO INDICA TRASLADO A SEMEFO"
	}
	if containsAnyNormalized(traslado, "ALBERGUE", "REFUGIO") {
		return "26", "PROTECCIÓN CIVIL: EL CAMPO TRASLADO INDICA TRASLADO A ALBERGUE O REFUGIO"
	}

	destination := normalizeClosureText(strings.TrimSpace(traslado + " " + recibe))
	healthDestination := containsAnyNormalized(destination,
		"HOSPITAL", "INSTITUCION DE SALUD", "CLINICA", "CENTRO DE SALUD", "URGENCIAS", "IMSS", "ISSSTE", "ISSTECH", "HGR", "HGZ")
	explicitTransfer := containsAnyNormalized(traslado, "SE TRASLADA", "SE TRASLADO", "TRASLADADO", "TRASLADADA", "AMERITA TRASLADO", "SI AMERITA") || healthDestination
	if explicitTransfer && healthDestination {
		if assessment {
			return "35", "PROTECCIÓN CIVIL: HAY VALORACIÓN/ATENCIÓN EN SITIO Y EL CAMPO TRASLADO/RECIBE CONFIRMA TRASLADO A UNA INSTITUCIÓN DE SALUD"
		}
		return "46", "PROTECCIÓN CIVIL: EL CAMPO TRASLADO/RECIBE CONFIRMA TRASLADO A UNA INSTITUCIÓN DE SALUD SIN EVIDENCIA OBJETIVA SUFICIENTE DE VALORACIÓN EN SITIO"
	}

	return "", ""
}

func closureProfile(note Note) (string, string) {
	corp := strings.ToUpper(strings.TrimSpace(note.Corporacion))
	switch corp {
	case "PC":
		return "PROTECCION_CIVIL", "PC — PROTECCIÓN CIVIL"
	case "TRVM":
		return "TRANSITO", "TRVM — TRÁNSITO VIAL MUNICIPAL"
	case "GEVP":
		return "TRANSITO", "GEVP — GUARDIA ESTATAL VIAL PREVENTIVA"
	case "SPM":
		return "SEGURIDAD", "SPM — SEGURIDAD PÚBLICA MUNICIPAL"
	case "GEP":
		return "SEGURIDAD", "GEP — GUARDIA ESTATAL PREVENTIVA"
	case "FRIP":
		return "SEGURIDAD", "FRIP — FUERZA DE REACCIÓN INMEDIATA PAKAL"
	default:
		return "GENERAL", "PERFIL GENERAL"
	}
}

func codeInList(code string, codes ...string) bool {
	for _, item := range codes {
		if code == item {
			return true
		}
	}
	return false
}

func closureCodeFamily(code string) string {
	switch {
	case codeInList(code, "17", "18", "25", "35", "44", "46", "47", "48"):
		return "MEDICO"
	case codeInList(code, "20", "21", "26", "30", "40", "75", "76", "77"):
		return "PROTECCION_CIVIL"
	case codeInList(code, "5", "15", "16", "23", "41", "45", "49", "53", "60", "63", "64", "65", "66", "67", "68", "69", "70", "74", "79"):
		return "SEGURIDAD"
	case codeInList(code, "42", "54", "55", "56", "57", "58", "59"):
		return "TRANSITO"
	default:
		return "GENERAL"
	}
}

func explicitMedicalContext(text string) bool {
	return containsAnyNormalized(text,
		"PROTECCION CIVIL", "AMBULANCIA", "UNIDAD MEDICA", "PARAMEDICO", "PARAMEDICA", "CRUZ ROJA",
		"SIGNOS VITALES", "GLASGOW", "ATENCION MEDICA", "VALORACION MEDICA", "ATENCION PREHOSPITALARIA")
}

func explicitProtectionCivilContext(text string) bool {
	return containsAnyNormalized(text,
		"PROTECCION CIVIL", "BOMBEROS", "INCENDIO", "PASTIZAL", "FUGA DE GAS", "DERRAME", "RESCATE", "EVACUACION", "ACORDONAMIENTO")
}

func explicitSecurityContext(text string) bool {
	return containsAnyNormalized(text,
		"POLICIA", "SEGURIDAD PUBLICA", "PATRULLA", "MINISTERIO PUBLICO", "FISCALIA", "DETENIDO", "DETENIDA",
		"FLAGRANCIA", "ORDEN DE APREHENSION", "ORDEN DE CATEO", "INDICIO DELICTIVO", "LABOR DE INTELIGENCIA")
}

func explicitTrafficContext(text string) bool {
	return containsAnyNormalized(text,
		"TRANSITO", "VIAL", "AGENTE DE TRANSITO", "CORRALON", "TARJETA DE INFRACCION", "INFRACCION",
		"CONDUCTOR", "CONDUCTORES", "ASEGURADORA", "ASEGURADORAS", "VEHICULO REMITIDO")
}

func profileAllowsCode(profile, code, text string) bool {
	family := closureCodeFamily(code)
	if family == "GENERAL" {
		return true
	}
	switch profile {
	case "PROTECCION_CIVIL":
		if family == "MEDICO" || family == "PROTECCION_CIVIL" {
			return true
		}
		if family == "SEGURIDAD" {
			return explicitSecurityContext(text)
		}
		if family == "TRANSITO" {
			return explicitTrafficContext(text)
		}
	case "SEGURIDAD":
		if family == "SEGURIDAD" {
			return true
		}
		if family == "MEDICO" {
			return explicitMedicalContext(text)
		}
		if family == "PROTECCION_CIVIL" {
			return explicitProtectionCivilContext(text)
		}
		if family == "TRANSITO" {
			return explicitTrafficContext(text)
		}
	case "TRANSITO":
		if family == "TRANSITO" {
			return true
		}
		if family == "SEGURIDAD" {
			return explicitSecurityContext(text)
		}
		if family == "MEDICO" {
			return explicitMedicalContext(text)
		}
		if family == "PROTECCION_CIVIL" {
			return explicitProtectionCivilContext(text)
		}
	default:
		return true
	}
	return false
}

func securityPersonDetainedOrSecured(text string) bool {
	return containsAnyNormalized(text,
		"PERSONA DETENIDA", "PERSONA DETENIDO", "PERSONA ASEGURADA", "PERSONA ASEGURADO",
		"FUE DETENIDA", "FUE DETENIDO", "FUE ASEGURADA", "FUE ASEGURADO",
		"SE ASEGURO A LA PERSONA", "SE REALIZO EL ASEGURAMIENTO DE LA PERSONA")
}

func securityPutAtDisposal(text string) bool {
	return containsAnyNormalized(text,
		"PUESTA A DISPOSICION", "PUESTO A DISPOSICION", "PONER A DISPOSICION",
		"PARA SER PUESTA A DISPOSICION", "PARA SER PUESTO A DISPOSICION",
		"QUEDO A DISPOSICION", "QUEDA A DISPOSICION", "FUE PUESTA A DISPOSICION", "FUE PUESTO A DISPOSICION")
}

func securityPMDestination(text string) bool {
	return containsAnyNormalized(text,
		"DISPOSICION DE LA P.M", "DISPOSICION DE POLICIA MUNICIPAL", "POLICIA MUNICIPAL",
		"SEGURIDAD PUBLICA MUNICIPAL", "INSTALACIONES DE SEGURIDAD PUBLICA MUNICIPAL",
		"COMANDANCIA MUNICIPAL", "BARANDILLA MUNICIPAL", "JUEZ CALIFICADOR", "AREA CORRESPONDIENTE")
}

func securityMPDestination(text string) bool {
	return containsAnyNormalized(text,
		"DISPOSICION DEL M.P", "DISPOSICION DEL MINISTERIO PUBLICO", "MINISTERIO PUBLICO",
		"FISCALIA", "FISCALIA DEL MINISTERIO PUBLICO", "AGENCIA DEL MINISTERIO PUBLICO")
}

func securityExplicitFlagrancyOrScene(text string) bool {
	return containsAnyNormalized(text,
		"FLAGRANCIA", "EN EL MOMENTO DE LOS HECHOS", "EN EL MOMENTO QUE SUCEDE",
		"DETENIDO EN EL LUGAR", "DETENIDA EN EL LUGAR", "ASEGURADO EN EL LUGAR", "ASEGURADA EN EL LUGAR",
		"EN EL SITIO", "EN EL LUGAR DE LOS HECHOS")
}

func structuredSecurityClosure(text string) (string, string) {
	// Primero los desenlaces más específicos; una detención posterior prevalece sobre una fuga inicial.
	if containsAnyNormalized(text, "DETENIDO EN FUGA", "DETENIDA EN FUGA", "DETENIDO CUANDO SE DABA A LA FUGA", "DETENIDA CUANDO SE DABA A LA FUGA") {
		if containsAnyNormalized(text, "MINISTERIO PUBLICO", "DISPOSICION DEL M.P") {
			return "74", "SEGURIDAD: PERSONA DETENIDA DURANTE LA FUGA Y PUESTA A DISPOSICIÓN DEL M.P."
		}
		if containsAnyNormalized(text, "POLICIA MUNICIPAL", "DISPOSICION DE LA P.M") {
			return "49", "SEGURIDAD: PERSONA DETENIDA DURANTE LA FUGA Y PUESTA A DISPOSICIÓN DE LA P.M."
		}
	}
	if containsAnyNormalized(text, "DETENIDO EN FLAGRANCIA", "DETENIDA EN FLAGRANCIA", "DETENCION EN FLAGRANCIA", "DETENIDO EN EL LUGAR", "DETENIDA EN EL LUGAR") {
		if containsAnyNormalized(text, "MINISTERIO PUBLICO", "DISPOSICION DEL M.P") {
			return "5", "SEGURIDAD: DETENCIÓN EN FLAGRANCIA Y PUESTA A DISPOSICIÓN DEL M.P."
		}
		if containsAnyNormalized(text, "POLICIA MUNICIPAL", "DISPOSICION DE LA P.M") {
			return "23", "SEGURIDAD: DETENCIÓN EN FLAGRANCIA Y PUESTA A DISPOSICIÓN DE LA P.M."
		}
	}
	// En las notas operativas de Seguridad Pública es común usar "persona asegurada" en lugar de
	// "persona detenida". Si además se documenta el traslado/puesta a disposición, el resultado
	// final es inequívoco para efectos de sugerencia. La ausencia de la palabra "flagrancia" solo
	// afecta el cierre automático (se revisa en trustedProfileClosure), no la sugerencia del código.
	if securityPersonDetainedOrSecured(text) && securityPutAtDisposal(text) {
		if securityMPDestination(text) {
			return "5", "SEGURIDAD: LA NARRATIVA CONFIRMA PERSONA DETENIDA/ASEGURADA Y PUESTA A DISPOSICIÓN DEL M.P."
		}
		if securityPMDestination(text) {
			return "23", "SEGURIDAD: LA NARRATIVA CONFIRMA PERSONA DETENIDA/ASEGURADA, TRASLADADA A SEGURIDAD PÚBLICA MUNICIPAL Y PUESTA A DISPOSICIÓN DE LA P.M."
		}
	}
	if containsAnyNormalized(text, "ORDEN DE APREHENSION") {
		return "45", "SEGURIDAD: LA NARRATIVA CONFIRMA EJECUCIÓN DE ORDEN DE APREHENSIÓN"
	}
	if containsAnyNormalized(text, "ORDEN DE CATEO", "DILIGENCIA DE CATEO") {
		return "53", "SEGURIDAD: LA NARRATIVA CONFIRMA EJECUCIÓN DE ORDEN DE CATEO"
	}
	if containsAnyNormalized(text, "DELINCUENTE MUERTO EN PERSECUCION", "RESPONSABLE FALLECIO DURANTE LA PERSECUCION") {
		return "16", "SEGURIDAD: EL RESPONSABLE FALLECIÓ DURANTE LA PERSECUCIÓN"
	}
	if containsAnyNormalized(text, "VEHICULO RECUPERADO", "SE RECUPERO EL VEHICULO", "SE LOCALIZO EL VEHICULO ROBADO") {
		return "41", "SEGURIDAD: EL VEHÍCULO REPORTADO FUE RECUPERADO"
	}
	if containsAnyNormalized(text, "PERSONA LOCALIZADA", "YA FUE LOCALIZADA", "FUE ENCONTRADA CON VIDA") {
		return "60", "SEGURIDAD: LA PERSONA REPORTADA FUE LOCALIZADA"
	}
	if containsAnyNormalized(text, "NO SE LOCALIZO A LA PERSONA OFENDIDA", "NO LOCALIZO A LA PERSONA OFENDIDA", "AGRAVIADO NO LOCALIZADO", "AGRAVIADA NO LOCALIZADA") {
		return "69", "SEGURIDAD: LA UNIDAD LLEGÓ Y NO LOCALIZÓ A LA PERSONA OFENDIDA"
	}
	if containsAnyNormalized(text, "NO ENCONTRO INDICIO DELICTIVO", "SIN INDICIOS DELICTIVOS", "NO SE CORROBORO EL HECHO DELICTIVO") {
		return "70", "SEGURIDAD: LA UNIDAD VERIFICÓ Y NO ENCONTRÓ INDICIOS DELICTIVOS"
	}
	if containsAnyNormalized(text, "LABOR DE INTELIGENCIA", "TRABAJOS DE INTELIGENCIA") {
		return "79", "SEGURIDAD: SE REALIZARON LABORES DE INTELIGENCIA"
	}
	if containsAnyNormalized(text, "AMONESTACION VERBAL", "LLAMADO DE ATENCION VERBAL") {
		return "6", "SEGURIDAD: ÚNICAMENTE SE REALIZÓ AMONESTACIÓN VERBAL"
	}
	if containsAnyNormalized(text, "RESPONSABLE SE DIO A LA FUGA", "RESPONSABLE SE DA A LA FUGA", "SE DIO A LA FUGA") && !containsAnyNormalized(text, "FUE DETENIDO", "FUE DETENIDA", "DETENIDO EN FUGA", "DETENIDA EN FUGA") {
		return "15", "SEGURIDAD: EL RESPONSABLE SE DIO A LA FUGA Y NO SE REPORTA DETENCIÓN POSTERIOR"
	}
	if containsAnyNormalized(text, "ALARMA ACTIVADA") && containsAnyNormalized(text, "SEGURIDAD PRIVADA", "CASA PARTICULAR", "NEGOCIO PARTICULAR", "CASA DE EMPENO") {
		return "63", "SEGURIDAD: ACTIVACIÓN DE ALARMA DE SEGURIDAD PRIVADA"
	}
	if containsAnyNormalized(text, "SEPROBAN") {
		if containsAnyNormalized(text, "APLICATIVO") {
			return "68", "SEGURIDAD: ALERTA SEPROBAN RECIBIDA MEDIANTE APLICATIVO"
		}
		if containsAnyNormalized(text, "GRABACION", "MENSAJE GRABADO") {
			return "66", "SEGURIDAD: ALERTA SEPROBAN RECIBIDA MEDIANTE GRABACIÓN"
		}
		if containsAnyNormalized(text, "LLAMADA", "OPERADOR") {
			return "67", "SEGURIDAD: ALERTA SEPROBAN RECIBIDA MEDIANTE LLAMADA"
		}
	}
	if containsAnyNormalized(text, "ALARMA", "CENTRAL DE ALARMAS") && containsAnyNormalized(text, "BANCO", "BANCARIA") {
		if containsAnyNormalized(text, "GRABACION", "GRABADORA", "CONMUTADOR") {
			return "65", "SEGURIDAD: CENTRAL DE ALARMAS BANCARIA MEDIANTE GRABACIÓN"
		}
		if containsAnyNormalized(text, "LLAMADA", "OPERADOR TELEFONICO") {
			return "64", "SEGURIDAD: CENTRAL DE ALARMAS BANCARIA MEDIANTE LLAMADA"
		}
	}
	return "", ""
}

func structuredTrafficClosure(text string) (string, string) {
	if containsAnyNormalized(text, "PERSONA DETENIDA Y VEHICULO REMITIDO", "DETENIDO Y VEHICULO REMITIDO", "DETENCION DEL CONDUCTOR Y REMISION DEL VEHICULO") ||
		(containsAnyNormalized(text, "DETENIDO", "DETENIDA") && containsAnyNormalized(text, "VEHICULO REMITIDO", "CORRALON")) {
		return "55", "TRÁNSITO: HUBO DETENCIÓN Y REMISIÓN DEL VEHÍCULO"
	}
	if containsAnyNormalized(text, "PERSONA FUGADA Y VEHICULO REMITIDO", "RESPONSABLE SE DIO A LA FUGA Y EL VEHICULO FUE REMITIDO") ||
		(strings.Contains(text, "FUGA") && containsAnyNormalized(text, "VEHICULO REMITIDO", "CORRALON")) {
		return "54", "TRÁNSITO: EL RESPONSABLE HUYÓ Y EL VEHÍCULO FUE REMITIDO"
	}
	if containsAnyNormalized(text, "SE ELABORO TARJETA DE INFRACCION", "TARJETA DE INFRACCION", "LEVANTO INFRACCION", "ELABORO INFRACCION") {
		return "56", "TRÁNSITO: SE ELABORÓ TARJETA DE INFRACCIÓN"
	}
	if containsAnyNormalized(text, "NO INTERVINO TRANSITO", "NO INTERVIENE EL AGENTE DE TRANSITO", "ARREGLO ENTRE PARTICULARES", "ARREGLO POR SU CUENTA") {
		return "58", "TRÁNSITO: LAS PARTES LLEGARON A UN ARREGLO POR SU CUENTA SIN INTERVENCIÓN DEL AGENTE"
	}
	if containsAnyNormalized(text, "ASEGURADORAS SE RESPONSABILIZAN", "ASEGURADORA SE RESPONSABILIZA", "SE HICIERON CARGO LAS ASEGURADORAS", "AJUSTADORES SE HICIERON CARGO") {
		return "59", "TRÁNSITO: LAS ASEGURADORAS SE RESPONSABILIZARON DE LOS DAÑOS"
	}
	if containsAnyNormalized(text, "CADA CONDUCTOR SE RESPONSABILIZO DE SUS DANOS", "CADA PARTE SE HIZO RESPONSABLE DE SUS DANOS", "CONDUCTORES SE RESPONSABILIZAN DE SUS DANOS") {
		return "57", "TRÁNSITO: CADA CONDUCTOR SE RESPONSABILIZÓ DE SUS DAÑOS"
	}
	if containsAnyNormalized(text, "VEHICULO REMITIDO", "VEHICULO FUE REMITIDO", "VEHICULO INVOLUCRADO FUE REMITIDO", "FUE REMITIDO AL CORRALON", "REMISION DEL VEHICULO") && containsAnyNormalized(text, "CORRALON", "FISCALIA", "PENSION") {
		return "42", "TRÁNSITO: EL VEHÍCULO FUE REMITIDO A CORRALÓN, PENSIÓN O FISCALÍA"
	}
	if containsAnyNormalized(text, "LLEGARON A UN CONVENIO", "SE LLEGO A UN CONVENIO", "CONVENIO ENTRE LAS PARTES") {
		return "39", "TRÁNSITO: LAS PARTES INVOLUCRADAS LLEGARON A UN CONVENIO"
	}
	return "", ""
}

func structuredProtectionCivilOperationalClosure(text string) (string, string) {
	if containsAnyNormalized(text, "QUEMA DE BASURA", "BASURA QUEMANDOSE") {
		return "77", "PROTECCIÓN CIVIL: EL FUEGO REPORTADO CORRESPONDIÓ A QUEMA DE BASURA"
	}
	if strings.Contains(text, "PASTIZAL") && containsAnyNormalized(text, "CONTROLADO", "SOFOCADO", "EXTINGUIDO") {
		return "76", "PROTECCIÓN CIVIL: EL INCENDIO DE PASTIZAL QUEDÓ CONTROLADO"
	}
	if containsAnyNormalized(text, "CONTROLADO POR EL PROPIETARIO", "SOFOCADO POR EL PROPIETARIO", "VECINOS CONTROLARON") {
		return "75", "PROTECCIÓN CIVIL: EL INCENDIO FUE CONTROLADO POR EL PROPIETARIO O VECINOS"
	}
	if containsAnyNormalized(text, "SE EXTINGUIO EL INCENDIO", "INCENDIO SOFOCADO", "SOFOCO EL INCENDIO", "SOFOCARON EL INCENDIO", "SE SOFOCARON LAS LLAMAS", "LLAMAS QUEDARON EXTINGUIDAS", "FUEGO EXTINGUIDO", "ERRADICA EL FUEGO") {
		return "21", "PROTECCIÓN CIVIL: LA CORPORACIÓN EXTINGUIÓ EL FUEGO"
	}
	if containsAnyNormalized(text, "FUGA DE GAS", "DERRAME DE GAS", "DERRAME DE SUSTANCIA") && containsAnyNormalized(text, "FUGA CONTROLADA", "FUGA CERRADA", "SE CERRO LA FUGA", "ERRADICO LA FUGA", "DERRAME CONTROLADO") {
		return "20", "PROTECCIÓN CIVIL: SE CONTROLÓ O CERRÓ LA FUGA/DERRAME REPORTADO"
	}
	if containsAnyNormalized(text, "SE REALIZO EL RESCATE", "PERSONA RESCATADA", "ANIMAL RESCATADO") {
		return "30", "PROTECCIÓN CIVIL: SE REALIZÓ EL RESCATE"
	}
	if containsAnyNormalized(text, "SE REALIZO EVACUACION", "FUERON EVACUADAS", "FUERON EVACUADOS", "EVACUACION DE PERSONAS") {
		return "40", "PROTECCIÓN CIVIL: SE REALIZÓ EVACUACIÓN DE PERSONAS"
	}
	if containsAnyNormalized(text, "SIMULACRO", "PRUEBA DE SISMO", "PRUEBA DE INCENDIO") {
		return "24", "PROTECCIÓN CIVIL: EL SERVICIO CORRESPONDIÓ A UN SIMULACRO O PRUEBA"
	}
	if containsAnyNormalized(text, "ASESORIA MEDICA", "PRIMEROS AUXILIOS POR TELEFONO", "ASISTENCIA TELEFONICA DE PRIMEROS AUXILIOS") {
		return "44", "PROTECCIÓN CIVIL: SE BRINDÓ ASESORÍA MÉDICA POR VÍA TELEFÓNICA"
	}
	return "", ""
}

func structuredCommonClosure(text string) (string, string) {
	if containsAnyNormalized(text, "SOLICITO CANCELAR EL APOYO", "YA NO REQUIERE EL APOYO", "REPORTANTE CANCELO", "INCIDENTE CANCELADO POR EL CIUDADANO") {
		return "32", "RESULTADO FINAL: EL CIUDADANO CANCELÓ EL APOYO"
	}
	if containsAnyNormalized(text, "INCIDENTE NO ATENDIDO POR FALTA DE UNIDADES", "FALTA DE UNIDADES", "NO HAY UNIDADES DISPONIBLES") {
		return "36", "RESULTADO FINAL: NO SE ATENDIÓ POR FALTA DE UNIDADES"
	}
	if containsAnyNormalized(text, "FALTA DE COMBUSTIBLE", "SIN GASOLINA") {
		return "71", "RESULTADO FINAL: LA UNIDAD NO ACUDIÓ POR FALTA DE COMBUSTIBLE"
	}
	if containsAnyNormalized(text, "UNIDADES CONCENTRADAS", "POR ORDEN SUPERIOR", "POR INSTRUCCIONES SUPERIORES") {
		return "73", "RESULTADO FINAL: LAS UNIDADES ESTABAN CONCENTRADAS POR ORDEN SUPERIOR"
	}
	if containsAnyNormalized(text, "UNIDAD NO LLEGO", "UNIDAD NO ACUDIO") && !containsAnyNormalized(text, "FALTA DE UNIDADES", "FALTA DE COMBUSTIBLE") {
		return "28", "RESULTADO FINAL: LA UNIDAD NO LLEGÓ Y NO SE REPORTA CAUSA ESPECÍFICA DE FALTA DE UNIDAD/COMBUSTIBLE"
	}
	if containsAnyNormalized(text, "DOMICILIO NO EXISTE", "DIRECCION INCORRECTA", "DOMICILIO INCORRECTO") {
		return "8", "RESULTADO FINAL: EL DOMICILIO NO EXISTE O ES INCORRECTO"
	}
	if containsAnyNormalized(text, "NO SE LOCALIZO AL REPORTANTE", "REPORTANTE NO SALIO", "NO FUE POSIBLE CONTACTAR AL REPORTANTE") {
		return "43", "RESULTADO FINAL: NO SE LOGRÓ CONTACTO CON EL INCIDENTE/REPORTANTE"
	}
	if containsAnyNormalized(text, "CANALIZADO A INSTANCIA COMPETENTE", "CANALIZACION A INSTANCIA COMPETENTE", "SE TURNO A LA INSTANCIA COMPETENTE") {
		return "52", "RESULTADO FINAL: EL CASO FUE CANALIZADO A LA INSTANCIA COMPETENTE"
	}
	if containsAnyNormalized(text, "APOYO ENTRE CORPORACIONES", "TRABAJARON EN CONJUNTO", "COORDINACION ENTRE CORPORACIONES") {
		return "22", "RESULTADO FINAL: HUBO COORDINACIÓN Y APOYO ENTRE CORPORACIONES"
	}
	if containsAnyNormalized(text, "ORIENTACION INFORMACION", "SE BRINDO ORIENTACION", "SE LE ORIENTO") {
		return "29", "RESULTADO FINAL: ÚNICAMENTE SE BRINDÓ ORIENTACIÓN O INFORMACIÓN"
	}
	if containsAnyNormalized(text, "APOYO PREVENTIVO", "PRESENCIA PREVENTIVA", "VIGILANCIA PREVENTIVA", "RECORRIDO PREVENTIVO") {
		return "7", "RESULTADO FINAL: SE BRINDÓ APOYO PREVENTIVO"
	}
	if containsAnyNormalized(text, "APOYO A LA CIUDADANIA", "SE BRINDO APOYO A LA CIUDADANIA") {
		return "4", "RESULTADO FINAL: SE BRINDÓ APOYO A LA CIUDADANÍA"
	}
	if containsAnyNormalized(text, "SE INFORMA A LA CORPORACION", "SE INFORMO A LA CORPORACION", "SE NOTIFICO A LA CORPORACION PARA SU CONOCIMIENTO") {
		return "13", "RESULTADO FINAL: SE INFORMÓ A LA CORPORACIÓN PARA CONOCIMIENTO/SEGUIMIENTO"
	}
	if containsAnyNormalized(text, "CONSIGNA", "QUEDA EN PANTALLA PARA MONITOREO", "SE MANTIENE MONITOREO") {
		return "38", "RESULTADO FINAL: EL INCIDENTE QUEDA EN CONSIGNA/MONITOREO"
	}
	if containsAnyNormalized(text, "ACORDONAMIENTO PREVENTIVO", "SE ACORDONO EL AREA", "PERIMETRO DE SEGURIDAD") {
		return "78", "RESULTADO FINAL: SE REALIZÓ ACORDONAMIENTO PREVENTIVO"
	}
	// HECHO FALSO exige ausencia explícita de indicios de lo reportado. "SIN NOVEDAD" por sí solo no basta.
	if containsAnyNormalized(text, "NO HAY INDICIOS SOBRE LA EMERGENCIA REPORTADA", "NO SE ENCONTRARON INDICIOS DE LO REPORTADO", "SIN INDICIOS DE LA EMERGENCIA REPORTADA") {
		return "2", "RESULTADO FINAL: NO SE ENCONTRARON INDICIOS DE LA EMERGENCIA REPORTADA"
	}
	return "", ""
}

func trustedProfileClosure(note Note, full, tail, outcome string) (string, string, string, string, bool) {
	profile, label := closureProfile(note)
	// Compatibilidad con notas antiguas que no guardaban corporación: inferir solo como contexto.
	if profile == "GENERAL" {
		rawAll := normalizeClosureText(strings.Join(plainNoteLines(note.ContenidoHTML), " "))
		pcSignals := 0
		for _, marker := range []string{"PROTECCION CIVIL", "AMBULANCIA", "JEFE DE SERVICIO", "CRONOMETRIA", "DATOS DEL PACIENTE", "SIGNOS VITALES", "TRASLADO"} {
			if strings.Contains(rawAll, marker) {
				pcSignals++
			}
		}
		if pcSignals >= 4 {
			profile, label = "PROTECCION_CIVIL", "PC — PROTECCIÓN CIVIL (DETECTADO POR ESTRUCTURA)"
		} else if explicitTrafficContext(rawAll) {
			profile, label = "TRANSITO", "TRÁNSITO/VIALIDAD (DETECTADO POR CONTEXTO)"
		} else if explicitSecurityContext(rawAll) {
			profile, label = "SEGURIDAD", "SEGURIDAD PÚBLICA (DETECTADO POR CONTEXTO)"
		}
	}
	text := strings.TrimSpace(outcome + " " + tail)

	// La plantilla clínica de Protección Civil tiene campos con significado propio (por ejemplo
	// "Traslado: No amerita"). Se interpreta primero para evitar que el nombre del campo se tome
	// como una acción realizada.
	if profile == "PROTECCION_CIVIL" {
		if code, reason := structuredProtectionCivilClosure(note); code != "" {
			return code, reason, profile, label, true
		}
	}

	// Motor principal 4.3: las 65 definiciones compiten con el mismo criterio semántico.
	// El perfil de corporación NO limita códigos; solo sirve como contexto/desempate.
	if code, reason, safe := definitionDrivenClosure(profile, text); code != "" {
		return code, reason, profile, label, safe
	}

	// Compatibilidad/fallback para redacciones muy antiguas que ya estaban cubiertas por reglas
	// estructuradas. No se ejecutan si el motor por definiciones encontró una coincidencia.
	var code, reason string
	safeDecision := false
	switch profile {
	case "PROTECCION_CIVIL":
		code, reason = structuredProtectionCivilOperationalClosure(text)
	case "SEGURIDAD":
		code, reason = structuredSecurityClosure(text)
		if code != "" {
			safeDecision = true
		}
		if codeInList(code, "5", "23") && securityPersonDetainedOrSecured(text) && securityPutAtDisposal(text) && !securityExplicitFlagrancyOrScene(text) {
			safeDecision = false
		}
	case "TRANSITO":
		code, reason = structuredTrafficClosure(text)
		if code != "" {
			safeDecision = true
		}
	}
	if code == "" {
		code, reason = structuredCommonClosure(text)
		if code != "" {
			safeDecision = false
		}
	}
	if code == "" {
		code, reason = decisiveClosure(outcome, text, full)
		if code != "" {
			safeDecision = false
		}
	}
	if code != "" {
		return code, reason, profile, label, safeDecision
	}
	return "", "", profile, label, false
}

func decisiveClosure(last, tail, full string) (string, string) {
	// Primero se resuelven resultados irreversibles y combinaciones específicas.
	// Así, por ejemplo, una muerte durante un traslado nunca queda clasificada como simple traslado.
	if containsAnyNormalized(tail, "TRASLADO A INSTITUCION DE SALUD Y MUERTO EN SITIO", "TRASLADADA A UNA INSTITUCION DE SALUD Y POSTERIOR MUERE EN EL LUGAR", "TRASLADADO A UNA INSTITUCION DE SALUD Y POSTERIOR MUERE EN EL LUGAR") {
		return "37", "LA NARRATIVA INDICA TRASLADO A UNA INSTITUCIÓN DE SALUD Y POSTERIOR MUERTE EN SITIO"
	}
	if containsAnyNormalized(tail, "FALLECIO EN EL HOSPITAL", "PERDIO LA VIDA EN EL HOSPITAL", "MUERTO EN INSTITUCION DE SALUD", "FALLECIO DENTRO DE LA INSTITUCION DE SALUD", "FALLECE DENTRO DE LA INSTITUCION DE SALUD") {
		return "48", "LA PERSONA FALLECIÓ EN UNA INSTITUCIÓN DE SALUD"
	}
	if containsAnyNormalized(tail, "FALLECIO DURANTE EL TRASLADO", "PERDIO LA VIDA DURANTE EL TRASLADO", "MUERTO EN TRASLADO", "REALIZA EL TRASLADO Y LA PARTE AFECTADA FALLECE") ||
		(containsAnyNormalized(tail, "DURANTE EL TRASLADO", "EN TRASLADO") && containsAnyNormalized(tail, "FALLECIO", "PERDIO LA VIDA", "DECESO")) {
		return "47", "LA PERSONA FALLECIÓ DURANTE EL TRASLADO"
	}
	if containsAnyNormalized(tail, "DELINCUENTE MUERTO EN PERSECUCION", "RESPONSABLE FALLECIO DURANTE LA PERSECUCION") || (strings.Contains(tail, "FUGA") && containsAnyNormalized(tail, "RESPONSABLE FALLECE", "RESPONSABLE FALLECIO")) {
		return "16", "EL RESPONSABLE FALLECIÓ DURANTE LA PERSECUCIÓN"
	}
	if containsAnyNormalized(tail, "SIN SIGNOS VITALES", "FALLECIO EN EL LUGAR", "MUERTO EN SITIO", "MUERE EN EL LUGAR DE LA EMERGENCIA", "PERSONA AFECTADA MUERE EN EL LUGAR") {
		return "19", "LA PERSONA FUE LOCALIZADA SIN SIGNOS VITALES EN EL SITIO"
	}

	// Reglas médicas prioritarias para diferenciar atención en sitio, traslado y ambas acciones.
	noTransfer := containsAnyNormalized(tail, "NO REQUIRIO TRASLADO", "NO REQUIERE TRASLADO", "NO AMERITO TRASLADO", "NO AMERITA TRASLADO", "TRASLADO NO AMERITA", "TRASLADO NO AMERITO", "TRASLADO NO REQUIERE", "TRASLADO NO REQUIRIO", "SIN TRASLADO", "NO FUE NECESARIO EL TRASLADO", "NO FUE TRASLADADO", "NO FUE TRASLADADA")
	transfer := !noTransfer && containsAnyNormalized(tail, "TRASLADADO", "TRASLADADA", "TRASLADO", "SE TRASLADA", "FUE TRASLADADO", "FUE TRASLADADA")
	hospital := containsAnyNormalized(tail, "HOSPITAL", "INSTITUCION DE SALUD", "CLINICA", "URGENCIAS", "HGR", "IMSS", "CENTRO DE SALUD")
	directMedical := containsAnyNormalized(tail,
		"VALORADO EN EL LUGAR", "VALORADA EN EL LUGAR", "SE VALORO EN EL LUGAR", "VALORACION EN EL SITIO",
		"ATENDIDO EN EL LUGAR", "ATENDIDA EN EL LUGAR", "ATENCION EN SITIO", "ATENCION EN EL SITIO",
		"ATENCION MEDICA EN SITIO", "ATENCION MEDICA EN EL SITIO", "ATENCION PREHOSPITALARIA EN EL SITIO",
		"ATENCION PREHOSPITALARIA EN EL LUGAR", "PRIMEROS AUXILIOS EN EL SITIO", "PRIMEROS AUXILIOS EN EL LUGAR",
		"FUE ESTABILIZADO EN EL LUGAR", "FUE ESTABILIZADA EN EL LUGAR", "SE ESTABILIZA EN EL LUGAR", "SE ESTABILIZO EN EL LUGAR", "SE LE BRINDO ATENCION PREHOSPITALARIA",
		"UNIDAD MEDICA ATIENDE O VALORA", "ATIENDE O VALORA A LA PARTE AFECTADA EN EL LUGAR", "VALORA A LA PARTE AFECTADA EN EL LUGAR")
	clinical := containsAnyNormalized(tail, "VALORACION PRIMARIA", "VALORACION PREHOSPITALARIA", "TOMA DE SIGNOS VITALES", "SIGNOS VITALES", "INMOVILIZACION", "CONTROL DE HEMORRAGIA", "CURACION", "VENDAJE", "ESTABILIZACION", "OXIGENO")
	arrival := containsAnyNormalized(tail, "AL ARRIBAR", "AL LLEGAR", "EN EL LUGAR", "EN EL SITIO")
	medicalSite := directMedical || (arrival && clinical)
	if containsAnyNormalized(tail, "SE NEGO A LA ATENCION", "SE NIEGA A SER VALORADA", "SE NIEGA A SER VALORADO", "SE NIEGA A SER ATENDIDA", "SE NIEGA A SER ATENDIDO", "RECHAZO LA ATENCION", "FIRMO NEGATIVA", "NO ACEPTO SER VALORADO", "NO ACEPTO SER VALORADA") {
		return "25", "LA PERSONA SE NEGÓ A SER VALORADA O ATENDIDA"
	}
	if containsAnyNormalized(tail, "TRASLADADO A UN ALBERGUE", "TRASLADADA A UN ALBERGUE", "TRASLADO AL ALBERGUE", "CANALIZADO A REFUGIO") {
		return "26", "LA PERSONA FUE TRASLADADA A UN ALBERGUE O REFUGIO"
	}
	if containsAnyNormalized(tail, "TRASLADO A SEMEFO", "TRASLADADO AL SEMEFO", "TRASLADADA AL SEMEFO", "SERVICIO MEDICO FORENSE") {
		return "27", "LA PERSONA OCCISA FUE TRASLADADA AL SEMEFO"
	}
	if containsAnyNormalized(tail, "TRASLADADO POR SUS PROPIOS MEDIOS", "TRASLADADA POR SUS PROPIOS MEDIOS", "TRASLADARA A LA PARTE AFECTADA POR SUS PROPIOS MEDIOS", "TRASLADARA POR SUS PROPIOS MEDIOS", "FAMILIARES LO TRASLADARON", "FAMILIARES LA TRASLADARON", "VEHICULO PARTICULAR") {
		return "34", "EL TRASLADO FUE REALIZADO POR UN PARTICULAR O POR SUS PROPIOS MEDIOS"
	}
	if medicalSite && transfer && hospital {
		return "35", "LA NARRATIVA CONFIRMA ATENCIÓN O VALORACIÓN EN SITIO Y POSTERIOR TRASLADO A UNA INSTITUCIÓN DE SALUD"
	}
	if transfer && hospital {
		return "46", "LA NARRATIVA CONFIRMA TRASLADO A UNA INSTITUCIÓN DE SALUD SIN EVIDENCIA SUFICIENTE DE ATENCIÓN EN SITIO"
	}
	if medicalSite && !transfer {
		return "17", "LA PERSONA FUE ATENDIDA O VALORADA EN EL SITIO SIN TRASLADO"
	}
	if containsAnyNormalized(tail, "ATENDIDO EN EL HOSPITAL", "ATENDIDA EN EL HOSPITAL", "RECIBIO ATENCION EN EL HOSPITAL", "ATENDIDO EN UNA INSTITUCION DE SALUD", "ATENDIDA EN UNA INSTITUCION DE SALUD") && !transfer {
		return "18", "LA PERSONA RECIBIÓ ATENCIÓN MÉDICA EN UNA INSTITUCIÓN DE SALUD"
	}
	if containsAnyNormalized(tail, "ASESORIA MEDICA", "PRIMEROS AUXILIOS VIA TELEFONICA", "INDICACIONES MEDICAS POR TELEFONO", "ASISTENCIA TELEFONICA") {
		return "44", "SE BRINDÓ ASESORÍA MÉDICA O PRIMEROS AUXILIOS POR VÍA TELEFÓNICA"
	}

	// Reglas combinadas de seguridad y tránsito que prevalecen sobre coincidencias simples.
	if containsAnyNormalized(tail, "PERSONA FUGADA Y VEHICULO REMITIDO", "RESPONSABLE SE DIO A LA FUGA Y EL VEHICULO FUE REMITIDO") {
		return "54", "EL RESPONSABLE HUYÓ Y EL VEHÍCULO FUE REMITIDO"
	}
	if containsAnyNormalized(tail, "PERSONA DETENIDA Y VEHICULO REMITIDO", "DETENIDO Y VEHICULO AL CORRALON", "DETENCION DEL CONDUCTOR Y REMISION DEL VEHICULO") {
		return "55", "HUBO DETENCIÓN Y REMISIÓN DEL VEHÍCULO"
	}
	if containsAnyNormalized(tail, "DETENIDO EN FUGA", "DETENIDO CUANDO SE DABA A LA FUGA") {
		if containsAnyNormalized(tail, "MINISTERIO PUBLICO", "DISPOSICION DEL M.P") {
			return "74", "LA PERSONA FUE DETENIDA EN FUGA Y PUESTA A DISPOSICIÓN DEL M.P."
		}
		if containsAnyNormalized(tail, "POLICIA MUNICIPAL", "DISPOSICION DE LA P.M") {
			return "49", "LA PERSONA FUE DETENIDA EN FUGA Y PUESTA A DISPOSICIÓN DE LA P.M."
		}
	}
	if containsAnyNormalized(tail, "DETENIDO EN FLAGRANCIA", "DETENCION EN FLAGRANCIA", "DETENIDO EN EL LUGAR") {
		if containsAnyNormalized(tail, "MINISTERIO PUBLICO", "DISPOSICION DEL M.P") {
			return "5", "LA PERSONA FUE DETENIDA EN FLAGRANCIA Y PUESTA A DISPOSICIÓN DEL M.P."
		}
		if containsAnyNormalized(tail, "POLICIA MUNICIPAL", "DISPOSICION DE LA P.M") {
			return "23", "LA PERSONA FUE DETENIDA EN FLAGRANCIA Y PUESTA A DISPOSICIÓN DE LA P.M."
		}
	}
	if containsAnyNormalized(tail, "ARREGLO ENTRE PARTICULARES", "NO INTERVINO TRANSITO", "LLEGARON A UN ARREGLO POR SU CUENTA") {
		return "58", "LAS PARTES LLEGARON A UN ARREGLO SIN INTERVENCIÓN DEL AGENTE DE TRÁNSITO"
	}
	if containsAnyNormalized(tail, "ASEGURADORAS SE RESPONSABILIZAN", "SE HICIERON CARGO LAS ASEGURADORAS", "AJUSTADORES SE HICIERON CARGO") {
		return "59", "LAS ASEGURADORAS SE RESPONSABILIZARON DE LOS DAÑOS"
	}
	if containsAnyNormalized(tail, "CADA CONDUCTOR SE RESPONSABILIZO DE SUS DANOS", "CADA PARTE SE HIZO RESPONSABLE DE SUS DANOS") {
		return "57", "LOS CONDUCTORES SE RESPONSABILIZARON DE SUS DAÑOS"
	}
	if containsAnyNormalized(tail, "SEPROBAN") {
		if containsAnyNormalized(tail, "APLICATIVO") {
			return "68", "LA ALERTA DE SEPROBAN FUE RECIBIDA MEDIANTE APLICATIVO"
		}
		if containsAnyNormalized(tail, "GRABACION", "MENSAJE GRABADO") {
			return "66", "LA ALERTA DE SEPROBAN FUE RECIBIDA MEDIANTE GRABACIÓN"
		}
		if containsAnyNormalized(tail, "LLAMADA", "OPERADOR") {
			return "67", "LA ALERTA DE SEPROBAN FUE RECIBIDA MEDIANTE LLAMADA"
		}
	}
	if containsAnyNormalized(tail, "ALARMA", "CENTRAL DE ALARMAS") && containsAnyNormalized(tail, "BANCO", "BANCARIA") {
		if containsAnyNormalized(tail, "GRABACION", "GRABADORA", "CONMUTADOR", "MENSAJE GRABADO") {
			return "65", "LA ALERTA BANCARIA FUE RECIBIDA MEDIANTE GRABACIÓN O CONMUTADOR"
		}
		if containsAnyNormalized(tail, "LLAMADA", "OPERADOR TELEFONICO") {
			return "64", "LA ALERTA BANCARIA FUE RECIBIDA MEDIANTE LLAMADA"
		}
	}

	if containsAnyNormalized(tail, "VEHICULO RECUPERADO", "SE ENCONTRO SU VEHICULO EN OTRO LUGAR", "REPORTANTE MANIFIESTE QUE SE ENCONTRO SU VEHICULO") {
		return "41", "EL VEHÍCULO REPORTADO FUE RECUPERADO"
	}
	if strings.Contains(tail, "FUGA") && containsAnyNormalized(tail, "VEHICULO ES REMITIDO", "VEHICULO FUE REMITIDO", "VEHICULO REMITIDO", "CORRALON", "FISCALIA") {
		return "54", "EL RESPONSABLE HUYÓ Y EL VEHÍCULO FUE REMITIDO"
	}
	if containsAnyNormalized(tail, "DETENCION DEL RESPONSABLE EN EL MOMENTO DE LA FUGA", "DETENCION DEL RESPONSABLE EN MOMENTO DE LA FUGA") && containsAnyNormalized(tail, "DISPOSICION DEL M.P", "MINISTERIO PUBLICO") {
		return "74", "LA PERSONA FUE DETENIDA EN FUGA Y PUESTA A DISPOSICIÓN DEL M.P."
	}

	// Resultados operativos frecuentes.
	if containsAnyNormalized(tail, "SOLICITO CANCELAR EL APOYO", "YA NO REQUIERE EL APOYO", "REPORTANTE CANCELO") {
		return "32", "EL REPORTANTE CANCELÓ EXPRESAMENTE EL APOYO"
	}
	if containsAnyNormalized(tail, "FALTA DE UNIDADES", "NO HAY UNIDADES DISPONIBLES") {
		return "36", "EL INCIDENTE NO FUE ATENDIDO POR FALTA DE UNIDADES"
	}
	if containsAnyNormalized(tail, "FALTA DE COMBUSTIBLE", "SIN GASOLINA") {
		return "71", "LA UNIDAD NO ACUDIÓ POR FALTA DE COMBUSTIBLE"
	}
	if containsAnyNormalized(tail, "UNIDADES CONCENTRADAS", "POR ORDEN SUPERIOR", "POR INSTRUCCIONES SUPERIORES") {
		return "73", "LAS UNIDADES ESTABAN CONCENTRADAS POR ORDEN SUPERIOR"
	}
	if containsAnyNormalized(tail, "QUEMA DE BASURA", "BASURA QUEMANDOSE") {
		return "77", "EL FUEGO REPORTADO CORRESPONDIÓ A QUEMA DE BASURA"
	}
	if strings.Contains(tail, "PASTIZAL") && containsAnyNormalized(tail, "CONTROLADO", "SOFOCADO", "EXTINGUIDO") {
		return "76", "EL INCENDIO DE PASTIZAL QUEDÓ CONTROLADO"
	}
	if containsAnyNormalized(tail, "CONTROLADO POR EL PROPIETARIO", "SOFOCADO POR EL PROPIETARIO", "VECINOS CONTROLARON") {
		return "75", "EL INCENDIO FUE CONTROLADO POR EL PROPIETARIO O VECINOS"
	}
	if containsAnyNormalized(tail, "SE EXTINGUIO EL INCENDIO", "INCENDIO SOFOCADO", "SE SOFOCARON LAS LLAMAS", "FUEGO EXTINGUIDO", "ERRADICA EL FUEGO") {
		return "21", "LA CORPORACIÓN EXTINGUIÓ EL FUEGO"
	}
	if containsAnyNormalized(tail, "DIRECCION INCORRECTA", "DOMICILIO NO EXISTE", "NO SE LOCALIZO EL DOMICILIO") {
		return "8", "EL DOMICILIO REPORTADO ES INCORRECTO O NO EXISTE"
	}
	if containsAnyNormalized(tail, "NO SE LOCALIZO AL REPORTANTE", "REPORTANTE NO SALIO", "NO FUE POSIBLE CONTACTAR AL REPORTANTE") {
		return "43", "NO SE LOGRÓ CONTACTO CON EL INCIDENTE O REPORTANTE"
	}
	if containsAnyNormalized(tail, "NO SE LOCALIZO A LA PERSONA OFENDIDA", "AGRAVIADO NO LOCALIZADO") {
		return "69", "LA UNIDAD NO LOCALIZÓ A LA PERSONA OFENDIDA"
	}
	if containsAnyNormalized(tail, "NO ENCONTRO INDICIO DELICTIVO", "SIN INDICIOS DELICTIVOS", "NO SE CORROBORO EL HECHO DELICTIVO") {
		return "70", "LA UNIDAD VERIFICÓ Y NO ENCONTRÓ INDICIOS DELICTIVOS"
	}
	if containsAnyNormalized(tail, "NO HAY INDICIOS SOBRE LA EMERGENCIA REPORTADA", "NO SE ENCONTRARON INDICIOS DE LO REPORTADO", "SIN INDICIOS DE LA EMERGENCIA REPORTADA") && !strings.Contains(tail, "DELICT") {
		return "2", "NO SE ENCONTRARON INDICIOS DE LA EMERGENCIA REPORTADA"
	}
	if containsAnyNormalized(tail, "SE REALIZO EL RESCATE", "PERSONA RESCATADA", "ANIMAL RESCATADO") {
		return "30", "LA CORPORACIÓN REALIZÓ UN RESCATE"
	}
	if containsAnyNormalized(tail, "PERSONA LOCALIZADA", "YA FUE LOCALIZADA", "FUE ENCONTRADA CON VIDA") {
		return "60", "LA PERSONA REPORTADA FUE LOCALIZADA"
	}
	if containsAnyNormalized(tail, "ACORDONAMIENTO PREVENTIVO", "SE ACORDONO EL AREA", "PERIMETRO DE SEGURIDAD") {
		return "78", "SE REALIZÓ ACORDONAMIENTO PREVENTIVO"
	}
	if containsAnyNormalized(tail, "LABOR DE INTELIGENCIA", "TRABAJOS DE INTELIGENCIA") {
		return "79", "SE REALIZARON LABORES DE INTELIGENCIA"
	}
	if containsAnyNormalized(tail, "APOYO PREVENTIVO", "RECORRIDOS PREVENTIVOS", "PRESENCIA PREVENTIVA", "VIGILANCIA PREVENTIVA") {
		return "7", "SE PROPORCIONÓ APOYO PREVENTIVO"
	}

	_ = last
	_ = full
	return "", ""
}

func closureContextAdjustment(code string, tail string) float64 {
	noTransfer := containsAnyNormalized(tail, "NO REQUIRIO TRASLADO", "NO REQUIERE TRASLADO", "NO AMERITO TRASLADO", "NO AMERITA TRASLADO", "TRASLADO NO AMERITA", "TRASLADO NO AMERITO", "TRASLADO NO REQUIERE", "TRASLADO NO REQUIRIO", "SIN TRASLADO", "NO FUE NECESARIO EL TRASLADO", "NO FUE TRASLADADO", "NO FUE TRASLADADA")
	hasTransfer := !noTransfer && containsAnyNormalized(tail, "TRASLADADO", "TRASLADADA", "TRASLADO", "SE TRASLADA")
	hasHospital := containsAnyNormalized(tail, "HOSPITAL", "INSTITUCION DE SALUD", "CLINICA", "URGENCIAS", "HGR", "IMSS")
	hasMedicalSite := containsAnyNormalized(tail, "VALORADO EN EL LUGAR", "VALORADA EN EL LUGAR", "ATENCION EN SITIO", "ATENCION EN EL SITIO", "ATENCION MEDICA EN SITIO", "ATENCION PREHOSPITALARIA", "PRIMEROS AUXILIOS", "VALORACION PRIMARIA", "SIGNOS VITALES", "INMOVILIZACION", "ESTABILIZACION", "SE ESTABILIZA EN EL LUGAR", "SE ESTABILIZO EN EL LUGAR")
	hasDeath := containsAnyNormalized(tail, "FALLECIO", "PERDIO LA VIDA", "SIN SIGNOS VITALES", "DECESO")
	score := 0.0
	switch code {
	case "17":
		if hasTransfer {
			score -= 28
		}
		if hasMedicalSite && !hasTransfer {
			score += 25
		}
	case "46":
		if !hasTransfer {
			score -= 18
		}
		if hasMedicalSite && hasTransfer && hasHospital {
			score -= 55
		}
		if hasTransfer && hasHospital && !hasMedicalSite {
			score += 25
		}
	case "35":
		if hasTransfer && hasHospital && hasMedicalSite {
			score += 45
		} else {
			score -= 35
		}
	case "19", "47", "48":
		if !hasDeath {
			score -= 30
		}
	}
	return score
}

func analyzeClosure(note Note) ClosureAnalysis {
	full, tail, outcome := closureTextSections(note)
	profile, profileLabel := closureProfile(note)
	analysis := ClosureAnalysis{Alternatives: []ClosureCandidate{}, Confidence: "BAJA", Reason: "NO HAY EVIDENCIA FINAL SUFICIENTE PARA SUGERIR UN CÓDIGO CON SEGURIDAD. SE REQUIERE SELECCIÓN HUMANA.", Profile: profile, ProfileLabel: profileLabel, SafeToAutoClose: false}
	if full == "" {
		return analysis
	}

	decisiveCode, decisiveReason, profile, profileLabel, safeDecision := trustedProfileClosure(note, full, tail, outcome)
	analysis.Profile = profile
	analysis.ProfileLabel = profileLabel
	analysis.SafeToAutoClose = safeDecision
	// Si la narrativa contiene literalmente una definición completa del catálogo,
	// esa coincidencia tiene prioridad absoluta. Esto también permite auditar que los 65 códigos
	// cargados desde el PDF sean reconocibles sin depender de una lista parcial de reglas.
	bestDefinitionLen := 0
	if decisiveCode == "" {
		for _, item := range closureCatalog {
			definition := normalizeClosureText(item.Definition)
			definitionLen := len([]rune(definition))
			if definitionLen >= 35 && strings.Contains(outcome, definition) && definitionLen > bestDefinitionLen {
				bestDefinitionLen = definitionLen
				decisiveCode = item.Code
				decisiveReason = "COINCIDENCIA CON LA DEFINICIÓN COMPLETA DEL CÓDIGO DE CIERRE " + item.Code
			}
		}
	}
	if decisiveCode == "" {
		candidateCode, candidateReason := directStrongClosure(outcome, tail)
		if candidateCode != "" && profileAllowsCode(profile, candidateCode, outcome+" "+tail) {
			decisiveCode, decisiveReason = candidateCode, candidateReason
			// Coincidencia textual directa sirve como sugerencia, pero no habilita cierre automático.
			analysis.SafeToAutoClose = false
		}
	}

	noteSemanticConcepts, noteSemanticEvidence := semanticConceptSet(outcome + " " + tail)
	candidates := make([]ClosureCandidate, 0, len(closureCatalog))
	semanticCoverageByCode := map[string]float64{}
	for _, item := range closureCatalog {
		score := 0.0
		evidence := []string{}
		conceptMatches := 0
		nameNeedle := normalizeClosureText(item.Name)
		if nameNeedle != "" {
			if strings.Contains(outcome, nameNeedle) {
				score += 48
				evidence = append(evidence, item.Name)
			} else if strings.Contains(tail, nameNeedle) {
				score += 28
				evidence = append(evidence, item.Name)
			}
		}

		for _, phrase := range item.Strong {
			needle := normalizeClosureText(phrase)
			if needle == "" {
				continue
			}
			if strings.Contains(outcome, needle) {
				score += 38
				evidence = append(evidence, phrase)
			} else if strings.Contains(tail, needle) {
				score += 23
				evidence = append(evidence, phrase)
			} else if strings.Contains(full, needle) {
				score += 7
			}
		}

		for _, concept := range item.Concepts {
			needle := normalizeClosureText(concept)
			if needle == "" {
				continue
			}
			if strings.Contains(outcome, needle) {
				score += 12
				conceptMatches++
				evidence = append(evidence, concept)
			} else if strings.Contains(tail, needle) {
				score += 7
				conceptMatches++
			} else if strings.Contains(full, needle) {
				score += 2
				conceptMatches++
			}
		}
		if len(item.Concepts) > 1 && conceptMatches >= len(item.Concepts) {
			score += 20
		}

		// La definición completa del PDF también participa. Solo se toman palabras
		// distintivas para evitar que frases de trámite como "de acuerdo a las notas" sesguen el resultado.
		definitionTokens := closureTokenSet(item.Definition)
		matchedDefinition := 0
		for token := range definitionTokens {
			if strings.Contains(outcome, token) {
				score += 4.5
				matchedDefinition++
			} else if strings.Contains(tail, token) {
				score += 2.0
				matchedDefinition++
			} else if strings.Contains(full, token) {
				score += .4
			}
		}
		if matchedDefinition >= 3 {
			score += float64(matchedDefinition-2) * 3.5
		}

		semanticScore, semanticEvidence, semanticCoverage := semanticDefinitionScore(item, noteSemanticConcepts, noteSemanticEvidence)
		semanticCoverageByCode[item.Code] = semanticCoverage
		if semanticScore > 0 {
			score += semanticScore
			for _, ev := range semanticEvidence {
				if len(evidence) >= 8 {
					break
				}
				evidence = append(evidence, "DEFINICIÓN/CONCEPTO "+ev)
			}
		}

		score += closureContextAdjustment(item.Code, outcome+" "+tail)
		if !profileAllowsCode(profile, item.Code, outcome+" "+tail) {
			score *= 0.08
		}
		if item.Code == decisiveCode {
			if score < 1000 {
				score = 1000
			}
			evidence = append([]string{decisiveReason}, evidence...)
		}
		if score < 0 {
			score = 0
		}
		if len(evidence) > 8 {
			evidence = evidence[:8]
		}
		candidates = append(candidates, ClosureCandidate{Code: item.Code, Name: item.Name, Definition: item.Definition, Score: score, Evidence: evidence})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			ci, _ := strconv.Atoi(candidates[i].Code)
			cj, _ := strconv.Atoi(candidates[j].Code)
			return ci < cj
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) == 0 || candidates[0].Score < 24 {
		return analysis
	}
	top := candidates[0]
	secondScore := 0.0
	if len(candidates) > 1 {
		secondScore = candidates[1].Score
	}
	margin := top.Score - secondScore
	trusted := decisiveCode != "" && top.Code == decisiveCode
	if trusted {
		analysis.Confidence = "ALTA"
		analysis.Recommended = &top
		analysis.Reason = decisiveReason
	} else if semanticCoverageByCode[top.Code] >= .78 && top.Score >= 105 && margin >= 18 && profileAllowsCode(profile, top.Code, outcome+" "+tail) {
		// La nota cubre la mayor parte de los conceptos de la definición oficial aunque use sinónimos.
		// Se recomienda con confianza ALTA para revisión, pero no se auto-cierra si no pasó una regla
		// estructurada de resultado final.
		analysis.Confidence = "ALTA"
		analysis.Recommended = &top
		analysis.SafeToAutoClose = false
		analysis.Reason = fmt.Sprintf("PERFIL %s: LA REDACCIÓN COINCIDE SEMÁNTICAMENTE CON LA DEFINICIÓN DEL CÓDIGO %s · %s (COBERTURA %.0f%%). SE RECONOCIERON SINÓNIMOS Y RELACIONES OPERATIVAS; REVISA ANTES DEL CIERRE AUTOMÁTICO.", profileLabel, top.Code, top.Name, semanticCoverageByCode[top.Code]*100)
	} else if top.Score >= 110 && margin >= 24 && profileAllowsCode(profile, top.Code, outcome+" "+tail) {
		// Una coincidencia estadística muy fuerte se muestra como MEDIA para revisión humana.
		analysis.Confidence = "MEDIA"
		analysis.SafeToAutoClose = false
		analysis.Reason = fmt.Sprintf("PERFIL %s: HAY UNA COINCIDENCIA POSIBLE CON CÓDIGO %s · %s, PERO FALTA UN RESULTADO FINAL INEQUÍVOCO. REVISA MANUALMENTE.", profileLabel, top.Code, top.Name)
	} else {
		analysis.Confidence = "BAJA"
		analysis.SafeToAutoClose = false
		analysis.Reason = "PERFIL " + profileLabel + ": NO HAY UN RESULTADO FINAL INEQUÍVOCO. EL SISTEMA NO ADIVINARÁ EL CÓDIGO; SE REQUIERE REVISIÓN MANUAL."
	}
	for _, candidate := range candidates {
		if candidate.Score < 14 || len(analysis.Alternatives) >= 6 {
			break
		}
		if analysis.Recommended != nil && candidate.Code == analysis.Recommended.Code {
			continue
		}
		analysis.Alternatives = append(analysis.Alternatives, candidate)
	}
	return analysis
}

func analyzeClosureHTTP(w http.ResponseWriter, r *http.Request, s *Store) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("note_id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Identificador de nota inválido.")
		return
	}
	s.mu.RLock()
	var note *Note
	for i := range s.db.Notes {
		if s.db.Notes[i].ID == id {
			copy := s.db.Notes[i]
			note = &copy
			break
		}
	}
	s.mu.RUnlock()
	if note == nil {
		writeError(w, http.StatusNotFound, "La nota no existe.")
		return
	}
	analysis := analyzeClosure(*note)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "analysis": analysis})
}

func workflowAction(w http.ResponseWriter, r *http.Request, s *Store) {
	var payload struct {
		NoteID      int64  `json:"noteId"`
		Action      string `json:"action"`
		ClosureCode string `json:"closureCode"`
		RequireCode bool   `json:"requireCode"`
		Observation string `json:"observation"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if payload.NoteID <= 0 {
		writeError(w, http.StatusBadRequest, "Identificador de nota inválido.")
		return
	}
	operator := dispatcherFromRequest(r)
	if operator == "" {
		writeError(w, http.StatusUnauthorized, "Inicia sesión de despacho para realizar esta acción.")
		return
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	s.mu.Lock()
	index := -1
	for i := range s.db.Notes {
		if s.db.Notes[i].ID == payload.NoteID {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "La nota no existe.")
		return
	}
	note := &s.db.Notes[index]
	now := nowISO()
	changed := false
	switch action {
	case "open":
		if note.WorkflowStatus == "" || note.WorkflowStatus == statusNew {
			note.WorkflowStatus = statusOpen
			if note.OpenedAt == "" {
				note.OpenedAt = now
			}
			note.UpdatedAt = now
			s.addAuditLocked("ABRIR_NOTA", note.ID, note.Folio, map[string]any{"operador": operator, "ip": clientIP(r)})
			changed = true
		}
	case "used":
		if note.WorkflowStatus == statusClosed {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "El incidente ya está cerrado.")
			return
		}
		note.WorkflowStatus = statusUsed
		note.UsedAt = now
		note.AutoCloseEligible = false
		note.UpdatedAt = now
		s.addAuditLocked("MARCAR_USADA", note.ID, note.Folio, map[string]any{"operador": operator, "ip": clientIP(r)})
		changed = true
	case "close":
		if note.WorkflowStatus == statusClosed {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "El incidente ya está cerrado.")
			return
		}
		code := nonDigits.ReplaceAllString(payload.ClosureCode, "")
		if payload.RequireCode && code == "" {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, "Selecciona un código de cierre o desmarca Código de cierre obligatorio.")
			return
		}
		closureName := ""
		if code != "" {
			item, ok := closureByCode[code]
			if !ok {
				s.mu.Unlock()
				writeError(w, http.StatusBadRequest, "El código de cierre seleccionado no existe en el catálogo.")
				return
			}
			closureName = item.Name
		}
		note.WorkflowStatus = statusClosed
		note.ClosedAt = now
		note.ClosureCode = code
		note.ClosureName = closureName
		if code == "" {
			note.ClosureMethod = "MANUAL SIN CÓDIGO"
		} else {
			note.ClosureMethod = "MANUAL"
		}
		note.ClosureReason = truncate(strings.TrimSpace(payload.Observation), 500)
		note.AutoCloseEligible = false
		note.UpdatedAt = now
		s.addAuditLocked("CERRAR_INCIDENTE", note.ID, note.Folio, map[string]any{
			"operador": operator, "ip": clientIP(r), "codigo": code, "nombre": closureName,
		})
		changed = true
	default:
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "Acción de seguimiento no válida.")
		return
	}
	if changed {
		s.db.Version++
		if err := s.saveLocked(); err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	copy := *note
	version := s.db.Version
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": copy, "version": version})
}

func autoCloseLoop(s *Store) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	time.Sleep(2 * time.Second)
	autoCloseOnce(s)
	for range ticker.C {
		autoCloseOnce(s)
	}
}

func autoCloseOnce(s *Store) {
	now := time.Now()
	s.mu.Lock()
	changed := false
	for i := range s.db.Notes {
		note := &s.db.Notes[i]
		if note.PhotoOnly || !note.AutoCloseEligible || note.UsedAt != "" || note.WorkflowStatus == statusClosed || note.WorkflowStatus == statusUsed {
			continue
		}
		created, err := time.Parse(time.RFC3339, note.CreatedAt)
		if err != nil || now.Sub(created) < autoCloseAfter {
			continue
		}
		analysis := analyzeClosure(*note)
		if analysis.Recommended == nil || analysis.Confidence != "ALTA" || !analysis.SafeToAutoClose {
			continue
		}
		item, ok := closureByCode[analysis.Recommended.Code]
		if !ok {
			continue
		}
		note.WorkflowStatus = statusClosed
		note.ClosedAt = now.Format(time.RFC3339)
		note.ClosureCode = item.Code
		note.ClosureName = item.Name
		note.ClosureMethod = "AUTOMÁTICO 30 MIN"
		note.ClosureReason = analysis.Reason
		note.AutoCloseEligible = false
		note.UpdatedAt = now.Format(time.RFC3339)
		s.addAuditLocked("AUTO_CERRAR_30_MIN", note.ID, note.Folio, map[string]any{
			"operador": "SISTEMA AUTOMÁTICO", "codigo": item.Code, "nombre": item.Name,
		})
		changed = true
	}
	if changed {
		s.db.Version++
		_ = s.saveLocked()
	}
	s.mu.Unlock()
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("Pánico HTTP: %v", recovered)
				writeError(w, http.StatusInternalServerError, "Ocurrió un error interno.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func readLocalStatus(client *http.Client, port int) map[string]any {
	addr := fmt.Sprintf("http://%s:%d/api/status", loopbackHost, port)
	resp, err := client.Get(addr)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}
	return data
}

func stopRunningInstances() {
	client := &http.Client{Timeout: 450 * time.Millisecond}
	stopped := false
	for port := firstPort; port <= lastPort; port++ {
		data := readLocalStatus(client, port)
		if data == nil {
			continue
		}
		app, _ := data["app"].(string)
		family, _ := data["family"].(string)
		isOurApp := app == "sistema-notas-local-v1" || family == appFamily || strings.HasPrefix(app, appFamily+"-")
		if !isOurApp {
			continue
		}

		shutdownURL := fmt.Sprintf("http://%s:%d/api/shutdown", loopbackHost, port)
		req, _ := http.NewRequest(http.MethodPost, shutdownURL, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			stopped = true
		}
	}
	if stopped {
		time.Sleep(950 * time.Millisecond)
	}
}

func findListener() (net.Listener, int, error) {
	for port := firstPort; port <= lastPort; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindHost, port))
		if err == nil {
			return listener, port, nil
		}
	}
	return nil, 0, errors.New("No se pudo abrir el servidor local. Cierre otras copias e intente nuevamente.")
}

func requestIsLocal(r *http.Request) bool {
	hostPart, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		hostPart = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(hostPart, "[]"))
	return ip != nil && ip.IsLoopback()
}

func networkAddresses(port int) []string {
	seen := map[string]bool{}
	result := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			address := fmt.Sprintf("http://%s:%d/", ip.String(), port)
			if !seen[address] {
				seen[address] = true
				result = append(result, address)
			}
		}
	}
	sort.Strings(result)
	return result
}

func openBrowser(address string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	case "darwin":
		cmd = exec.Command("open", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	_ = cmd.Start()
}

func showFatal(message string) {
	log.Printf("Error fatal: %s", message)
	if runtime.GOOS == "windows" {
		_ = exec.Command("mshta", "javascript:alert('"+strings.ReplaceAll(message, "'", "")+"');close()").Run()
	}
}

func init() {
	mime.AddExtensionType(".js", "application/javascript")
	mime.AddExtensionType(".css", "text/css")
	loadEmbeddedCatalogs()
}
