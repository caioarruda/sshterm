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

type SSHClient struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	mu      sync.Mutex
}

type TerminalOutput struct {
	widget.BaseWidget
	lines  []string
	scroll *widget.Scroll
	mu     sync.Mutex
}

func newTerminalOutput() *TerminalOutput {
	t := &TerminalOutput{}
	t.ExtendBaseWidget(t)
	return t
}

func (t *TerminalOutput) CreateRenderer() fyne.WidgetRenderer {
	label := widget.NewRichTextFromMarkdown("")
	label.Wrapping = fyne.TextWrapWord
	return widget.NewSimpleRenderer(label)
}

type ConnectForm struct {
	host     *widget.Entry
	port     *widget.Entry
	user     *widget.Entry
	password *widget.Entry
	keyPath  *widget.Entry
}

type App struct {
	fyneApp    fyne.App
	win        fyne.Window
	sshClient  *SSHClient
	term       *widget.TextGrid
	termScroll *container.Scroll
	input      *widget.Entry
	statusBar  *widget.Label
	remoteDir  *widget.Entry
	connectBtn *widget.Button
	form       *ConnectForm
	termBuf    strings.Builder
	termMu     sync.Mutex
}

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	myApp := &App{
		fyneApp:   a,
		sshClient: &SSHClient{},
	}

	myApp.win = a.NewWindow("SSH Terminal")
	myApp.win.Resize(fyne.NewSize(1024, 768))
	myApp.buildUI()
	myApp.win.ShowAndRun()
}

func (a *App) buildUI() {
	a.form = &ConnectForm{
		host:     widget.NewEntry(),
		port:     widget.NewEntry(),
		user:     widget.NewEntry(),
		password: widget.NewEntry(),
		keyPath:  widget.NewEntry(),
	}
	a.form.host.SetPlaceHolder("hostname ou IP")
	a.form.port.SetText("22")
	a.form.user.SetPlaceHolder("usuario")
	a.form.password.Password = true
	a.form.password.SetPlaceHolder("senha (ou vazio para usar chave)")
	a.form.keyPath.SetPlaceHolder("~/.ssh/id_rsa (opcional)")

	keyBrowseBtn := widget.NewButton("...", func() {
		dialog.ShowFileOpen(func(uc fyne.URIReadCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			a.form.keyPath.SetText(uc.URI().Path())
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

	connectForm := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Host", a.form.host),
			widget.NewFormItem("Porta", a.form.port),
			widget.NewFormItem("Usuário", a.form.user),
			widget.NewFormItem("Senha", a.form.password),
			widget.NewFormItem("Chave SSH", container.NewBorderLayout(nil, nil, nil, keyBrowseBtn,
				a.form.keyPath, keyBrowseBtn)),
		),
		a.connectBtn,
	)

	a.term = widget.NewTextGrid()
	a.term.ShowLineNumbers = false
	a.termScroll = container.NewScroll(a.term)
	a.termScroll.SetMinSize(fyne.NewSize(700, 400))

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

	inputRow := container.NewBorderLayout(nil, nil, nil, sendBtn, a.input, sendBtn)

	a.remoteDir = widget.NewEntry()
	a.remoteDir.SetText("~")
	a.remoteDir.SetPlaceHolder("diretório remoto destino")

	dropArea := newDropTarget(a)
	dropLabel := canvas.NewText("↓ Arraste arquivos aqui para copiar via SCP", theme.ForegroundColor())
	dropLabel.TextStyle = fyne.TextStyle{Italic: true}
	dropLabel.Alignment = fyne.TextAlignCenter

	dropBox := container.NewStack(dropArea, container.NewCenter(dropLabel))

	a.statusBar = widget.NewLabel("Desconectado")
	a.statusBar.Importance = widget.LowImportance

	termPanel := container.NewBorder(
		nil,
		container.NewVBox(
			inputRow,
			container.NewBorder(nil, nil, widget.NewLabel("Destino SCP:"), nil, a.remoteDir),
			dropBox,
			a.statusBar,
		),
		nil, nil,
		a.termScroll,
	)

	split := container.NewHSplit(connectForm, termPanel)
	split.SetOffset(0.28)

	a.win.SetContent(split)
}

func (a *App) setStatus(msg string) {
	a.statusBar.SetText(msg)
}

func (a *App) appendTerm(text string) {
	a.termMu.Lock()
	defer a.termMu.Unlock()
	a.termBuf.WriteString(text)
	full := a.termBuf.String()
	lines := strings.Split(full, "\n")
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
		a.termBuf.Reset()
		a.termBuf.WriteString(strings.Join(lines, "\n"))
	}
	a.term.SetText(a.termBuf.String())
	a.termScroll.ScrollToBottom()
}

func (a *App) connect() {
	host := strings.TrimSpace(a.form.host.Text)
	port := strings.TrimSpace(a.form.port.Text)
	user := strings.TrimSpace(a.form.user.Text)
	password := a.form.password.Text
	keyPath := strings.TrimSpace(a.form.keyPath.Text)

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
			key, err := os.ReadFile(keyPath)
			if err == nil {
				signer, err := ssh.ParsePrivateKey(key)
				if err == nil {
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
					key, err := os.ReadFile(filepath.Join(home, ".ssh", name))
					if err != nil {
						continue
					}
					signer, err := ssh.ParsePrivateKey(key)
					if err != nil {
						continue
					}
					authMethods = append(authMethods, ssh.PublicKeys(signer))
					break
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
			a.fyneApp.SendNotification(&fyne.Notification{Title: "SSH", Content: err.Error()})
			fyne.Do(func() {
				a.setStatus(fmt.Sprintf("Erro: %v", err))
				a.connectBtn.Enable()
			})
			return
		}

		session, err := client.NewSession()
		if err != nil {
			client.Close()
			fyne.Do(func() {
				a.setStatus(fmt.Sprintf("Erro de sessão: %v", err))
				a.connectBtn.Enable()
			})
			return
		}

		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty("xterm-256color", 50, 180, modes); err != nil {
			session.Close()
			client.Close()
			fyne.Do(func() {
				a.setStatus(fmt.Sprintf("Erro PTY: %v", err))
				a.connectBtn.Enable()
			})
			return
		}

		stdin, err := session.StdinPipe()
		if err != nil {
			session.Close()
			client.Close()
			fyne.Do(func() {
				a.setStatus(fmt.Sprintf("Erro stdin: %v", err))
				a.connectBtn.Enable()
			})
			return
		}

		pr, pw := io.Pipe()
		session.Stdout = pw
		session.Stderr = pw

		if err := session.Shell(); err != nil {
			session.Close()
			client.Close()
			fyne.Do(func() {
				a.setStatus(fmt.Sprintf("Erro shell: %v", err))
				a.connectBtn.Enable()
			})
			return
		}

		a.sshClient.mu.Lock()
		a.sshClient.client = client
		a.sshClient.session = session
		a.sshClient.stdin = stdin
		a.sshClient.mu.Unlock()

		fyne.Do(func() {
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
					text := string(buf[:n])
					text = stripANSI(text)
					fyne.Do(func() { a.appendTerm(text) })
				}
				if err != nil {
					break
				}
			}
			fyne.Do(func() {
				a.setStatus("Sessão encerrada")
				a.connectBtn.SetText("Conectar")
				a.connectBtn.Importance = widget.HighImportance
				a.sshClient.mu.Lock()
				a.sshClient.client = nil
				a.sshClient.session = nil
				a.sshClient.stdin = nil
				a.sshClient.mu.Unlock()
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

	remoteDir := strings.TrimSpace(a.remoteDir.Text)
	if remoteDir == "" {
		remoteDir = "~"
	}

	go func() {
		filename := filepath.Base(path)
		fyne.Do(func() { a.setStatus(fmt.Sprintf("Enviando %s...", filename)) })

		f, err := os.Open(path)
		if err != nil {
			fyne.Do(func() { a.setStatus(fmt.Sprintf("Erro ao abrir arquivo: %v", err)) })
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			fyne.Do(func() { a.setStatus(fmt.Sprintf("Erro stat: %v", err)) })
			return
		}

		session, err := client.NewSession()
		if err != nil {
			fyne.Do(func() { a.setStatus(fmt.Sprintf("Erro de sessão SCP: %v", err)) })
			return
		}
		defer session.Close()

		stdin, err := session.StdinPipe()
		if err != nil {
			return
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- session.Run(fmt.Sprintf("scp -t %s", remoteDir))
		}()

		fmt.Fprintf(stdin, "C0644 %d %s\n", info.Size(), filename)
		io.Copy(stdin, f)
		fmt.Fprint(stdin, "\x00")
		stdin.Close()

		if err := <-errCh; err != nil {
			fyne.Do(func() { a.setStatus(fmt.Sprintf("Erro SCP: %v", err)) })
			return
		}

		fyne.Do(func() {
			a.setStatus(fmt.Sprintf("✓ %s enviado para %s", filename, remoteDir))
			a.appendTerm(fmt.Sprintf("\n[SCP] %s → %s/%s\n", filename, remoteDir, filename))
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

type DropTarget struct {
	widget.BaseWidget
	app        *App
	hovered    bool
}

func newDropTarget(a *App) *DropTarget {
	d := &DropTarget{app: a}
	d.ExtendBaseWidget(d)
	return d
}

func (d *DropTarget) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(theme.InputBorderColor())
	rect.CornerRadius = 8
	rect.StrokeColor = theme.PrimaryColor()
	rect.StrokeWidth = 2
	rect.FillColor = theme.InputBackgroundColor()
	rect.SetMinSize(fyne.NewSize(400, 60))
	return widget.NewSimpleRenderer(rect)
}

func (d *DropTarget) Dragged(ev *fyne.DragEvent) {}
func (d *DropTarget) DragEnd()                   {}

func (d *DropTarget) Tapped(*fyne.PointEvent) {
	dialog.ShowFileOpen(func(uc fyne.URIReadCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		d.app.uploadFile(uc.URI().Path())
	}, d.app.win)
}

func (d *DropTarget) MouseIn(*desktop.MouseEvent) {
	d.hovered = true
	d.Refresh()
}

func (d *DropTarget) MouseOut() {
	d.hovered = false
	d.Refresh()
}

func (d *DropTarget) MouseMoved(*desktop.MouseEvent) {}

// Fyne v2.5+ drop interface
func (d *DropTarget) AcceptsDrop(ev *fyne.URIDropEvent) bool {
	return true
}

func (d *DropTarget) Dropped(ev *fyne.URIDropEvent) {
	for _, uri := range ev.URIs {
		d.app.uploadFile(uri.Path())
	}
}
