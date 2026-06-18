package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	sqDialog "github.com/sqweek/dialog"
	"golang.org/x/crypto/ssh"
)

const pwdMarker = "__SSHTERM_PWD__:"

type SSHClient struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	mu      sync.Mutex
}

type App struct {
	fyneApp    fyne.App
	win        fyne.Window
	sshClient  *SSHClient
	termWidget *TermWidget
	statusBar  *widget.Label
	pwdLabel   *widget.Label
	connectBtn *widget.Button
	host       *widget.Entry
	port       *widget.Entry
	user       *widget.Entry
	password   *widget.Entry
	keyPath    *widget.Entry
	uiQueue    chan func()
	termBuf    strings.Builder
	termMu     sync.Mutex
	currentPwd string
	pwdMu      sync.Mutex
}

// TermWidget — focusable, keystrokes go straight to SSH stdin
type TermWidget struct {
	widget.BaseWidget
	app     *App
	content string
	mu      sync.Mutex
}

func newTermWidget(a *App) *TermWidget {
	t := &TermWidget{app: a}
	t.ExtendBaseWidget(t)
	return t
}

func (t *TermWidget) CreateRenderer() fyne.WidgetRenderer {
	label := widget.NewTextGrid()
	label.ShowLineNumbers = false
	return &termRenderer{label: label, term: t}
}

type termRenderer struct {
	label *widget.TextGrid
	term  *TermWidget
}

func (r *termRenderer) Layout(size fyne.Size)        { r.label.Resize(size) }
func (r *termRenderer) MinSize() fyne.Size           { return fyne.NewSize(400, 200) }
func (r *termRenderer) Refresh()                     { r.label.SetText(r.term.content); r.label.Refresh() }
func (r *termRenderer) Destroy()                     {}
func (r *termRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.label} }

func (t *TermWidget) FocusGained()               {}
func (t *TermWidget) FocusLost()                 {}
func (t *TermWidget) TypedRune(r rune)           { t.sendRaw(string(r)) }
func (t *TermWidget) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyReturn, fyne.KeyEnter:
		t.sendRaw("\r")
	case fyne.KeyBackspace:
		t.sendRaw("\x7f")
	case fyne.KeyDelete:
		t.sendRaw("\x1b[3~")
	case fyne.KeyUp:
		t.sendRaw("\x1b[A")
	case fyne.KeyDown:
		t.sendRaw("\x1b[B")
	case fyne.KeyRight:
		t.sendRaw("\x1b[C")
	case fyne.KeyLeft:
		t.sendRaw("\x1b[D")
	case fyne.KeyTab:
		t.sendRaw("\t")
	case fyne.KeyEscape:
		t.sendRaw("\x1b")
	case fyne.KeyHome:
		t.sendRaw("\x1b[H")
	case fyne.KeyEnd:
		t.sendRaw("\x1b[F")
	case fyne.KeyPageUp:
		t.sendRaw("\x1b[5~")
	case fyne.KeyPageDown:
		t.sendRaw("\x1b[6~")
	}
}

func (t *TermWidget) TypedShortcut(s fyne.Shortcut) {
	if sc, ok := s.(*desktop.CustomShortcut); ok && sc.Modifier == fyne.KeyModifierControl {
		switch sc.KeyName {
		case fyne.KeyC:
			t.sendRaw("\x03")
		case fyne.KeyD:
			t.sendRaw("\x04")
		case fyne.KeyZ:
			t.sendRaw("\x1a")
		case fyne.KeyL:
			t.sendRaw("\x0c")
		case fyne.KeyA:
			t.sendRaw("\x01")
		case fyne.KeyE:
			t.sendRaw("\x05")
		case fyne.KeyU:
			t.sendRaw("\x15")
		case fyne.KeyK:
			t.sendRaw("\x0b")
		case fyne.KeyW:
			t.sendRaw("\x17")
		}
	}
}

func (t *TermWidget) sendRaw(s string) {
	t.app.sshClient.mu.Lock()
	stdin := t.app.sshClient.stdin
	t.app.sshClient.mu.Unlock()
	if stdin != nil {
		fmt.Fprint(stdin, s)
	}
}

func (t *TermWidget) Tapped(*fyne.PointEvent) {
	t.app.win.Canvas().Focus(t)
}

func (t *TermWidget) setText(text string) {
	t.mu.Lock()
	t.content = text
	t.mu.Unlock()
	t.Refresh()
}

// DroppedFiles handles DnD from Fyne's own drag events (Linux/macOS)
func (t *TermWidget) DroppedFiles(uris []fyne.URI) {
	for _, uri := range uris {
		t.app.uploadFile(uri.Path())
	}
}

func (t *TermWidget) Dragged(*fyne.DragEvent) {}
func (t *TermWidget) DragEnd()                {}

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	myApp := &App{
		fyneApp:    a,
		sshClient:  &SSHClient{},
		uiQueue:    make(chan func(), 256),
		currentPwd: "~",
	}

	myApp.win = a.NewWindow("SSH Terminal")
	myApp.win.Resize(fyne.NewSize(1100, 700))
	myApp.buildUI()

	go func() {
		tick := time.NewTicker(16 * time.Millisecond)
		for range tick.C {
			for {
				select {
				case fn := <-myApp.uiQueue:
					fn()
				default:
					goto done
				}
			}
		done:
		}
	}()

	// Register Win32 WM_DROPFILES after window is shown
	myApp.win.Show()
	if hwnd := GetHWND(myApp.win); hwnd != 0 {
		RegisterDropTarget(hwnd, myApp)
	}

	myApp.fyneApp.Run()
}

func (a *App) ui(fn func()) { a.uiQueue <- fn }

func (a *App) buildUI() {
	a.host = widget.NewEntry()
	a.host.SetPlaceHolder("hostname ou IP")
	a.port = widget.NewEntry()
	a.port.SetText("22")
	a.user = widget.NewEntry()
	a.user.SetPlaceHolder("usuario")
	a.password = widget.NewEntry()
	a.password.Password = true
	a.password.SetPlaceHolder("senha")
	a.keyPath = widget.NewEntry()
	a.keyPath.SetPlaceHolder("~/.ssh/id_rsa")

	keyBrowseBtn := widget.NewButton("…", func() {
		go func() {
			path, err := sqDialog.File().Title("Chave SSH").Load()
			if err != nil || path == "" {
				return
			}
			a.ui(func() { a.keyPath.SetText(path) })
		}()
	})

	a.connectBtn = widget.NewButton("Conectar", func() {
		if a.sshClient.client != nil {
			a.disconnect()
		} else {
			a.connect()
		}
	})
	a.connectBtn.Importance = widget.HighImportance

	uploadBtn := widget.NewButton("⬆ Enviar", func() {
		a.sshClient.mu.Lock()
		connected := a.sshClient.client != nil
		a.sshClient.mu.Unlock()
		if !connected {
			dialog.ShowError(fmt.Errorf("não conectado"), a.win)
			return
		}
		go func() {
			path, err := sqDialog.File().Title("Enviar via SCP").Load()
			if err != nil || path == "" {
				return
			}
			a.uploadFile(path)
		}()
	})

	a.pwdLabel = widget.NewLabelWithStyle("~", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})

	sidePanel := container.NewVBox(
		widget.NewLabelWithStyle("Conexão", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewForm(
			widget.NewFormItem("Host", a.host),
			widget.NewFormItem("Porta", a.port),
			widget.NewFormItem("Usuário", a.user),
			widget.NewFormItem("Senha", a.password),
			widget.NewFormItem("Chave", container.NewBorder(nil, nil, nil, keyBrowseBtn, a.keyPath)),
		),
		a.connectBtn,
		widget.NewSeparator(),
		uploadBtn,
		widget.NewSeparator(),
		widget.NewLabel("Diretório remoto:"),
		a.pwdLabel,
	)

	a.termWidget = newTermWidget(a)
	termScroll := container.NewScroll(a.termWidget)

	a.statusBar = widget.NewLabel("Desconectado")
	a.statusBar.Importance = widget.LowImportance

	dropHint := canvas.NewText("◀ arraste arquivos para a janela", theme.DisabledColor())
	dropHint.TextSize = 10

	termPanel := container.NewBorder(
		nil,
		container.NewBorder(nil, nil, dropHint, nil, a.statusBar),
		nil, nil,
		termScroll,
	)

	split := container.NewHSplit(sidePanel, termPanel)
	split.SetOffset(0.22)
	a.win.SetContent(split)
	a.win.Canvas().Focus(a.termWidget)
}

func (a *App) setStatus(msg string) { a.statusBar.SetText(msg) }

func (a *App) setPwd(pwd string) {
	a.pwdMu.Lock()
	a.currentPwd = pwd
	a.pwdMu.Unlock()
	a.pwdLabel.SetText(pwd)
}

func (a *App) getPwd() string {
	a.pwdMu.Lock()
	defer a.pwdMu.Unlock()
	return a.currentPwd
}

func (a *App) filterAndAppend(raw string) {
	lines := strings.Split(raw, "\n")
	var keep []string
	for _, line := range lines {
		if idx := strings.Index(line, pwdMarker); idx >= 0 {
			pwd := strings.TrimSpace(line[idx+len(pwdMarker):])
			if pwd != "" {
				p := pwd
				a.ui(func() { a.setPwd(p) })
			}
		} else {
			keep = append(keep, line)
		}
	}
	filtered := strings.Join(keep, "\n")
	if filtered == "" {
		return
	}
	a.termMu.Lock()
	defer a.termMu.Unlock()
	a.termBuf.WriteString(filtered)
	allLines := strings.Split(a.termBuf.String(), "\n")
	if len(allLines) > 2000 {
		allLines = allLines[len(allLines)-2000:]
		a.termBuf.Reset()
		a.termBuf.WriteString(strings.Join(allLines, "\n"))
	}
	text := a.termBuf.String()
	a.ui(func() { a.termWidget.setText(text) })
}

func (a *App) connect() {
	host := strings.TrimSpace(a.host.Text)
	port := strings.TrimSpace(a.port.Text)
	user := strings.TrimSpace(a.user.Text)
	password := a.password.Text
	keyPath := strings.TrimSpace(a.keyPath.Text)

	if host == "" || user == "" {
		dialog.ShowError(fmt.Errorf("host e usuário são obrigatórios"), a.win)
		return
	}

	a.setStatus("Conectando...")
	a.connectBtn.Disable()

	go func() {
		var authMethods []ssh.AuthMethod

		if keyPath != "" {
			if strings.HasPrefix(keyPath, "~/") {
				home, _ := os.UserHomeDir()
				keyPath = filepath.Join(home, keyPath[2:])
			}
			if key, err := os.ReadFile(keyPath); err == nil {
				if signer, err := ssh.ParsePrivateKey(key); err == nil {
					authMethods = append(authMethods, ssh.PublicKeys(signer))
				}
			}
		}
		if password != "" {
			authMethods = append(authMethods, ssh.Password(password))
		}
		if len(authMethods) == 0 {
			if home, err := os.UserHomeDir(); err == nil {
				for _, name := range []string{"id_rsa", "id_ed25519", "id_ecdsa"} {
					if key, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
						if signer, err := ssh.ParsePrivateKey(key); err == nil {
							authMethods = append(authMethods, ssh.PublicKeys(signer))
							break
						}
					}
				}
			}
		}

		cfg := &ssh.ClientConfig{
			User:            user,
			Auth:            authMethods,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}

		addr := net.JoinHostPort(host, port)
		client, err := ssh.Dial("tcp", addr, cfg)
		if err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro: %v", err)); a.connectBtn.Enable() })
			return
		}

		session, err := client.NewSession()
		if err != nil {
			client.Close()
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro sessão: %v", err)); a.connectBtn.Enable() })
			return
		}

		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
		if err := session.RequestPty("xterm-256color", 50, 200, modes); err != nil {
			session.Close(); client.Close()
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro PTY: %v", err)); a.connectBtn.Enable() })
			return
		}

		stdin, err := session.StdinPipe()
		if err != nil {
			session.Close(); client.Close()
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro stdin: %v", err)); a.connectBtn.Enable() })
			return
		}

		pr, pw := io.Pipe()
		session.Stdout = pw
		session.Stderr = pw

		if err := session.Shell(); err != nil {
			session.Close(); client.Close()
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro shell: %v", err)); a.connectBtn.Enable() })
			return
		}

		a.sshClient.mu.Lock()
		a.sshClient.client = client
		a.sshClient.session = session
		a.sshClient.stdin = stdin
		a.sshClient.mu.Unlock()

		fmt.Fprintf(stdin, "export PROMPT_COMMAND='echo \"%s$(pwd)\"'\n", pwdMarker)

		a.ui(func() {
			a.setStatus(fmt.Sprintf("Conectado — %s@%s", user, addr))
			a.connectBtn.SetText("Desconectar")
			a.connectBtn.Importance = widget.DangerImportance
			a.connectBtn.Enable()
			a.win.Canvas().Focus(a.termWidget)
		})

		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := pr.Read(buf)
				if n > 0 {
					a.filterAndAppend(stripANSI(string(buf[:n])))
				}
				if err != nil {
					break
				}
			}
			a.ui(func() {
				a.setStatus("Sessão encerrada")
				a.connectBtn.SetText("Conectar")
				a.connectBtn.Importance = widget.HighImportance
				a.sshClient.mu.Lock()
				a.sshClient.client = nil
				a.sshClient.session = nil
				a.sshClient.stdin = nil
				a.sshClient.mu.Unlock()
				a.setPwd("~")
			})
		}()
	}()
}

func (a *App) disconnect() {
	a.sshClient.mu.Lock()
	defer a.sshClient.mu.Unlock()
	if a.sshClient.session != nil {
		a.sshClient.session.Close()
	}
	if a.sshClient.client != nil {
		a.sshClient.client.Close()
	}
	a.sshClient.client = nil
	a.sshClient.session = nil
	a.sshClient.stdin = nil
	a.connectBtn.SetText("Conectar")
	a.connectBtn.Importance = widget.HighImportance
	a.setStatus("Desconectado")
	a.setPwd("~")
}

// uploadFile sends a file via SCP to the current remote pwd.
// Uses "scp -t" protocol; the remote side overwrites if file exists.
func (a *App) uploadFile(path string) {
	a.sshClient.mu.Lock()
	client := a.sshClient.client
	a.sshClient.mu.Unlock()

	if client == nil {
		a.ui(func() { a.setStatus("Não conectado — arraste após conectar") })
		return
	}

	remoteDir := a.getPwd()

	go func() {
		filename := filepath.Base(path)
		a.ui(func() { a.setStatus(fmt.Sprintf("Enviando %s → %s ...", filename, remoteDir)) })

		f, err := os.Open(path)
		if err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro ao abrir: %v", err)) })
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro stat: %v", err)) })
			return
		}

		session, err := client.NewSession()
		if err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro sessão SCP: %v", err)) })
			return
		}
		defer session.Close()

		scpStdin, err := session.StdinPipe()
		if err != nil {
			return
		}

		errCh := make(chan error, 1)
		// scp -t destino — o protocolo SCP sobrescreve automaticamente se o arquivo existir
		go func() { errCh <- session.Run(fmt.Sprintf("scp -t %q", remoteDir)) }()

		// handshake: espera ACK (0x00) antes de enviar — simplificado, servidor geralmente aceita direto
		fmt.Fprintf(scpStdin, "C0644 %d %s\n", info.Size(), filename)
		io.Copy(scpStdin, f)
		fmt.Fprint(scpStdin, "\x00")
		scpStdin.Close()

		if err := <-errCh; err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro SCP: %v", err)) })
			return
		}

		a.ui(func() {
			msg := fmt.Sprintf("✓ %s → %s", filename, remoteDir)
			a.setStatus(msg)
			a.termMu.Lock()
			a.termBuf.WriteString(fmt.Sprintf("\r\n[SCP] %s\r\n", msg))
			text := a.termBuf.String()
			a.termMu.Unlock()
			a.termWidget.setText(text)
		})
	}()
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				inEsc = false
			}
			continue
		}
		if c == '\r' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
