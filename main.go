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
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	fyneterm "github.com/fyne-io/terminal"
	sqDialog "github.com/sqweek/dialog"
	"golang.org/x/crypto/ssh"
)

const pwdMarker = "__SSHTERM_PWD__:"

type SSHClient struct {
	client  *ssh.Client
	session *ssh.Session
	mu      sync.Mutex
}

type App struct {
	fyneApp    fyne.App
	win        fyne.Window
	sshClient  *SSHClient
	term       *fyneterm.Terminal
	sshStdin   io.WriteCloser
	statusBar  *widget.Label
	pwdLabel   *widget.Label
	connectBtn *widget.Button
	host       *widget.Entry
	port       *widget.Entry
	user       *widget.Entry
	password   *widget.Entry
	keyPath    *widget.Entry
	currentPwd string
	pwdMu      sync.Mutex
}

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	myApp := &App{
		fyneApp:    a,
		sshClient:  &SSHClient{},
		currentPwd: "~",
	}

	myApp.win = a.NewWindow("SSH Terminal")
	myApp.win.Resize(fyne.NewSize(1100, 700))
	myApp.buildUI()

	myApp.win.Show()
	if hwnd := GetHWND(myApp.win); hwnd != 0 {
		RegisterDropTarget(hwnd, myApp)
	}

	myApp.fyneApp.Run()
}

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
			a.keyPath.SetText(path)
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

	uploadBtn := widget.NewButton("⬆ Enviar arquivo", func() {
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
	a.statusBar = widget.NewLabel("Desconectado")
	a.statusBar.Importance = widget.LowImportance

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
		widget.NewLabel("Ou arraste arquivos para a janela"),
		widget.NewSeparator(),
		widget.NewLabel("Diretório remoto:"),
		a.pwdLabel,
	)

	a.term = fyneterm.New()

	termPanel := container.NewBorder(
		nil,
		a.statusBar,
		nil, nil,
		a.term,
	)

	split := container.NewHSplit(sidePanel, termPanel)
	split.SetOffset(0.22)
	a.win.SetContent(split)
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
			a.setStatus(fmt.Sprintf("Erro: %v", err))
			a.connectBtn.Enable()
			return
		}

		session, err := client.NewSession()
		if err != nil {
			client.Close()
			a.setStatus(fmt.Sprintf("Erro sessão: %v", err))
			a.connectBtn.Enable()
			return
		}

		modes := ssh.TerminalModes{
			ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty("xterm-256color", 50, 200, modes); err != nil {
			session.Close(); client.Close()
			a.setStatus(fmt.Sprintf("Erro PTY: %v", err))
			a.connectBtn.Enable()
			return
		}

		termReader, termWriter := io.Pipe()
		sshReader, sshWriter := io.Pipe()

		session.Stdin = termReader
		session.Stdout = sshWriter
		session.Stderr = sshWriter

		if err := session.Shell(); err != nil {
			session.Close(); client.Close()
			a.setStatus(fmt.Sprintf("Erro shell: %v", err))
			a.connectBtn.Enable()
			return
		}

		a.sshClient.mu.Lock()
		a.sshClient.client = client
		a.sshClient.session = session
		a.sshClient.mu.Unlock()

		a.sshStdin = termWriter

		go a.trackPwd(client)

		go func() {
			a.term.RunWithConnection(termWriter, sshReader)
		}()

		a.setStatus(fmt.Sprintf("Conectado — %s@%s", user, addr))
		a.connectBtn.SetText("Desconectar")
		a.connectBtn.Importance = widget.DangerImportance
		a.connectBtn.Enable()

		go func() {
			session.Wait()
			a.setStatus("Sessão encerrada")
			a.connectBtn.SetText("Conectar")
			a.connectBtn.Importance = widget.HighImportance
			a.sshClient.mu.Lock()
			a.sshClient.client = nil
			a.sshClient.session = nil
			a.sshClient.mu.Unlock()
			a.setPwd("~")
			termWriter.Close()
			sshWriter.Close()
		}()
	}()
}

func (a *App) trackPwd(client *ssh.Client) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.sshClient.mu.Lock()
		alive := a.sshClient.client == client
		a.sshClient.mu.Unlock()
		if !alive {
			return
		}
		sess, err := client.NewSession()
		if err != nil {
			return
		}
		out, err := sess.Output("pwd")
		sess.Close()
		if err != nil {
			continue
		}
		pwd := strings.TrimSpace(string(out))
		if pwd != "" {
			a.setPwd(pwd)
		}
	}
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
	a.connectBtn.SetText("Conectar")
	a.connectBtn.Importance = widget.HighImportance
	a.setStatus("Desconectado")
	a.setPwd("~")
}

func (a *App) uploadFile(path string) {
	a.sshClient.mu.Lock()
	client := a.sshClient.client
	a.sshClient.mu.Unlock()

	if client == nil {
		a.setStatus("Não conectado")
		return
	}

	remoteDir := a.getPwd()

	go func() {
		filename := filepath.Base(path)
		a.setStatus(fmt.Sprintf("Enviando %s → %s ...", filename, remoteDir))

		f, err := os.Open(path)
		if err != nil {
			a.setStatus(fmt.Sprintf("Erro ao abrir: %v", err))
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			a.setStatus(fmt.Sprintf("Erro stat: %v", err))
			return
		}

		session, err := client.NewSession()
		if err != nil {
			a.setStatus(fmt.Sprintf("Erro sessão SCP: %v", err))
			return
		}
		defer session.Close()

		scpStdin, err := session.StdinPipe()
		if err != nil {
			return
		}

		errCh := make(chan error, 1)
		go func() { errCh <- session.Run(fmt.Sprintf("scp -t %q", remoteDir)) }()

		fmt.Fprintf(scpStdin, "C0644 %d %s\n", info.Size(), filename)
		io.Copy(scpStdin, f)
		fmt.Fprint(scpStdin, "\x00")
		scpStdin.Close()

		if err := <-errCh; err != nil {
			a.setStatus(fmt.Sprintf("Erro SCP: %v", err))
			return
		}

		a.setStatus(fmt.Sprintf("✓ %s → %s", filename, remoteDir))
	}()
}
