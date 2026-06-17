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
	term       *widget.TextGrid
	termScroll *container.Scroll
	input      *widget.Entry
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
		t := time.NewTicker(16 * time.Millisecond)
		for range t.C {
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

	myApp.win.ShowAndRun()
}

func (a *App) ui(fn func()) { a.uiQueue <- fn }

func (a *App) buildUI() {
	// --- connection panel ---
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
		dialog.ShowFileOpen(func(uc fyne.URIReadCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			a.keyPath.SetText(uc.URI().Path())
		}, a.win)
	})

	a.connectBtn = widget.NewButton("Conectar", func() {
		if a.sshClient.client != nil {
			a.disconnect()
		} else {
			a.connect()
		}
	})
	a.connectBtn.Importance = widget.HighImportance

	sidePanel := container.NewVBox(
		widget.NewLabelWithStyle("SSH Terminal", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewForm(
			widget.NewFormItem("Host", a.host),
			widget.NewFormItem("Porta", a.port),
			widget.NewFormItem("Usuário", a.user),
			widget.NewFormItem("Senha", a.password),
			widget.NewFormItem("Chave", container.NewBorder(nil, nil, nil, keyBrowseBtn, a.keyPath)),
		),
		a.connectBtn,
	)

	// --- terminal ---
	a.term = widget.NewTextGrid()
	a.term.ShowLineNumbers = false
	a.termScroll = container.NewScroll(a.term)

	// --- input row ---
	a.input = widget.NewEntry()
	a.input.SetPlaceHolder("comando...")
	a.input.OnSubmitted = func(s string) {
		a.sendCommand(s)
		a.input.SetText("")
	}
	sendBtn := widget.NewButton("↵", func() {
		a.sendCommand(a.input.Text)
		a.input.SetText("")
	})

	// --- upload button + pwd indicator ---
	uploadBtn := widget.NewButton("⬆ Enviar arquivo", func() {
		a.sshClient.mu.Lock()
		connected := a.sshClient.client != nil
		a.sshClient.mu.Unlock()
		if !connected {
			dialog.ShowError(fmt.Errorf("não conectado"), a.win)
			return
		}
		dialog.ShowFileOpen(func(uc fyne.URIReadCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			a.uploadFile(uc.URI().Path())
		}, a.win)
	})
	uploadBtn.Importance = widget.MediumImportance

	a.pwdLabel = widget.NewLabelWithStyle("pwd: ~", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})

	dropArea := newDropTarget(a)
	dropHint := canvas.NewText("ou arraste aqui", theme.DisabledColor())
	dropHint.TextSize = 11
	dropHint.Alignment = fyne.TextAlignCenter

	uploadRow := container.NewBorder(nil, nil,
		container.NewHBox(uploadBtn, dropArea, dropHint),
		nil,
		a.pwdLabel,
	)

	// --- status bar ---
	a.statusBar = widget.NewLabelWithStyle("Desconectado", fyne.TextAlignLeading, fyne.TextStyle{})
	a.statusBar.Importance = widget.LowImportance

	termPanel := container.NewBorder(
		nil,
		container.NewVBox(
			widget.NewSeparator(),
			container.NewBorder(nil, nil, nil, sendBtn, a.input),
			uploadRow,
			a.statusBar,
		),
		nil, nil,
		a.termScroll,
	)

	split := container.NewHSplit(sidePanel, termPanel)
	split.SetOffset(0.25)
	a.win.SetContent(split)
}

func (a *App) setStatus(msg string) { a.statusBar.SetText(msg) }

func (a *App) setPwd(pwd string) {
	a.pwdMu.Lock()
	a.currentPwd = pwd
	a.pwdMu.Unlock()
	a.pwdLabel.SetText("pwd: " + pwd)
}

func (a *App) getPwd() string {
	a.pwdMu.Lock()
	defer a.pwdMu.Unlock()
	return a.currentPwd
}

// filterAndAppend removes pwd-probe lines from terminal output and updates pwd state.
func (a *App) filterAndAppend(raw string) {
	lines := strings.Split(raw, "\n")
	var keep []string
	for _, line := range lines {
		if idx := strings.Index(line, pwdMarker); idx >= 0 {
			pwd := strings.TrimSpace(line[idx+len(pwdMarker):])
			if pwd != "" {
				a.ui(func() { a.setPwd(pwd) })
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
	a.ui(func() {
		a.term.SetText(text)
		a.termScroll.ScrollToBottom()
	})
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

		modes := ssh.TerminalModes{
			ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
		}
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

		// inject PROMPT_COMMAND to track pwd silently
		fmt.Fprintf(stdin, "export PROMPT_COMMAND='echo \"%s$(pwd)\"'\n", pwdMarker)

		a.ui(func() {
			a.setStatus(fmt.Sprintf("Conectado: %s@%s", user, addr))
			a.connectBtn.SetText("Desconectar")
			a.connectBtn.Importance = widget.DangerImportance
			a.connectBtn.Enable()
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

func (a *App) sendCommand(cmd string) {
	a.sshClient.mu.Lock()
	stdin := a.sshClient.stdin
	a.sshClient.mu.Unlock()
	if stdin == nil {
		a.setStatus("Não conectado")
		return
	}
	fmt.Fprintf(stdin, "%s\n", cmd)
}

func (a *App) uploadFile(path string) {
	a.sshClient.mu.Lock()
	client := a.sshClient.client
	a.sshClient.mu.Unlock()

	if client == nil {
		dialog.ShowError(fmt.Errorf("não conectado"), a.win)
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

		stdin, err := session.StdinPipe()
		if err != nil {
			return
		}

		errCh := make(chan error, 1)
		go func() { errCh <- session.Run(fmt.Sprintf("scp -t %s", remoteDir)) }()

		fmt.Fprintf(stdin, "C0644 %d %s\n", info.Size(), filename)
		io.Copy(stdin, f)
		fmt.Fprint(stdin, "\x00")
		stdin.Close()

		if err := <-errCh; err != nil {
			a.ui(func() { a.setStatus(fmt.Sprintf("Erro SCP: %v", err)) })
			return
		}

		a.ui(func() {
			msg := fmt.Sprintf("✓ %s enviado para %s", filename, remoteDir)
			a.setStatus(msg)
			a.termMu.Lock()
			a.termBuf.WriteString(fmt.Sprintf("\n[SCP] %s\n", msg))
			text := a.termBuf.String()
			a.termMu.Unlock()
			a.term.SetText(text)
			a.termScroll.ScrollToBottom()
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

// DropTarget — clicável; DroppedFiles para DnD nativo (funciona no driver desktop do Fyne)
type DropTarget struct {
	widget.BaseWidget
	app *App
}

func newDropTarget(a *App) *DropTarget {
	d := &DropTarget{app: a}
	d.ExtendBaseWidget(d)
	return d
}

func (d *DropTarget) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(theme.InputBorderColor())
	rect.CornerRadius = 6
	rect.StrokeColor = theme.PrimaryColor()
	rect.StrokeWidth = 1
	rect.FillColor = theme.InputBackgroundColor()
	rect.SetMinSize(fyne.NewSize(120, 32))
	return widget.NewSimpleRenderer(rect)
}

func (d *DropTarget) Dragged(ev *fyne.DragEvent) {}
func (d *DropTarget) DragEnd()                   {}
func (d *DropTarget) MouseIn(*desktop.MouseEvent)    {}
func (d *DropTarget) MouseOut()                      {}
func (d *DropTarget) MouseMoved(*desktop.MouseEvent) {}

func (d *DropTarget) DroppedFiles(uris []fyne.URI) {
	for _, uri := range uris {
		d.app.uploadFile(uri.Path())
	}
}
