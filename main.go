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
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	sqDialog "github.com/sqweek/dialog"
	"golang.org/x/crypto/ssh"
)

const pwdMarker = "__SSHTERM_PWD__:"

// TermBuffer holds a grid of cells and processes VT100/xterm sequences.
type cell struct {
	ch rune
}

type TermBuffer struct {
	mu      sync.Mutex
	cols    int
	rows    int
	cells   [][]cell
	curX    int
	curY    int
	scrollY int // top line of scroll region (unused for now, full scroll)
}

func newTermBuffer(cols, rows int) *TermBuffer {
	t := &TermBuffer{cols: cols, rows: rows}
	t.cells = make([][]cell, rows)
	for i := range t.cells {
		t.cells[i] = make([]cell, cols)
		for j := range t.cells[i] {
			t.cells[i][j].ch = ' '
		}
	}
	return t
}

func (t *TermBuffer) scrollUp() {
	t.cells = append(t.cells[1:], make([]cell, t.cols))
	last := t.cells[len(t.cells)-1]
	for i := range last {
		last[i].ch = ' '
	}
	if t.curY > 0 {
		t.curY--
	}
}

func (t *TermBuffer) ensureCursor() {
	if t.curY >= t.rows {
		for t.curY >= t.rows {
			t.scrollUp()
		}
	}
	if t.curX >= t.cols {
		t.curX = t.cols - 1
	}
}

func (t *TermBuffer) putRune(r rune) {
	t.ensureCursor()
	t.cells[t.curY][t.curX].ch = r
	t.curX++
	if t.curX >= t.cols {
		t.curX = 0
		t.curY++
		if t.curY >= t.rows {
			t.scrollUp()
		}
	}
}

// Write processes raw bytes including VT100 escape sequences.
func (t *TermBuffer) Write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := 0
	for i < len(p) {
		b := p[i]
		switch {
		case b == '\r':
			t.curX = 0
		case b == '\n':
			t.curY++
			if t.curY >= t.rows {
				t.scrollUp()
			}
		case b == '\b' || b == 0x7f:
			if t.curX > 0 {
				t.curX--
				t.ensureCursor()
				t.cells[t.curY][t.curX].ch = ' '
			}
		case b == '\t':
			next := (t.curX/8 + 1) * 8
			if next >= t.cols {
				next = t.cols - 1
			}
			for t.curX < next {
				t.putRune(' ')
			}
		case b == 0x1b:
			// ESC sequence
			i++
			if i >= len(p) {
				break
			}
			switch p[i] {
			case '[':
				// CSI sequence: ESC [ <params> <cmd>
				i++
				start := i
				for i < len(p) && (p[i] == ';' || (p[i] >= '0' && p[i] <= '9')) {
					i++
				}
				if i >= len(p) {
					i--
					break
				}
				params := string(p[start:i])
				cmd := p[i]
				t.handleCSI(params, cmd)
			case ']':
				// OSC — skip until ST (BEL or ESC\)
				i++
				for i < len(p) {
					if p[i] == 0x07 {
						break
					}
					if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\' {
						i++
						break
					}
					i++
				}
			case '(':
				i++ // skip charset designation
			case 'M':
				// reverse index
				if t.curY > 0 {
					t.curY--
				}
			}
		case b >= 0x20:
			t.putRune(rune(b))
		}
		i++
	}
}

func (t *TermBuffer) handleCSI(params string, cmd byte) {
	nums := parseParams(params)
	n0 := func(def int) int {
		if len(nums) > 0 && nums[0] > 0 {
			return nums[0]
		}
		return def
	}
	n1 := func(def int) int {
		if len(nums) > 1 && nums[1] > 0 {
			return nums[1]
		}
		return def
	}
	switch cmd {
	case 'A': // cursor up
		t.curY -= n0(1)
		if t.curY < 0 {
			t.curY = 0
		}
	case 'B': // cursor down
		t.curY += n0(1)
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case 'C': // cursor forward
		t.curX += n0(1)
		if t.curX >= t.cols {
			t.curX = t.cols - 1
		}
	case 'D': // cursor back
		t.curX -= n0(1)
		if t.curX < 0 {
			t.curX = 0
		}
	case 'H', 'f': // cursor position
		t.curY = n0(1) - 1
		t.curX = n1(1) - 1
		if t.curY < 0 {
			t.curY = 0
		}
		if t.curX < 0 {
			t.curX = 0
		}
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
		if t.curX >= t.cols {
			t.curX = t.cols - 1
		}
	case 'J': // erase display
		switch n0(0) {
		case 0: // from cursor to end
			for x := t.curX; x < t.cols; x++ {
				t.cells[t.curY][x].ch = ' '
			}
			for y := t.curY + 1; y < t.rows; y++ {
				for x := range t.cells[y] {
					t.cells[y][x].ch = ' '
				}
			}
		case 1: // from start to cursor
			for y := 0; y < t.curY; y++ {
				for x := range t.cells[y] {
					t.cells[y][x].ch = ' '
				}
			}
		case 2, 3: // whole screen
			for y := range t.cells {
				for x := range t.cells[y] {
					t.cells[y][x].ch = ' '
				}
			}
			t.curX, t.curY = 0, 0
		}
	case 'K': // erase line
		switch n0(0) {
		case 0:
			for x := t.curX; x < t.cols; x++ {
				t.cells[t.curY][x].ch = ' '
			}
		case 1:
			for x := 0; x <= t.curX; x++ {
				t.cells[t.curY][x].ch = ' '
			}
		case 2:
			for x := range t.cells[t.curY] {
				t.cells[t.curY][x].ch = ' '
			}
		}
	case 'P': // delete chars
		n := n0(1)
		row := t.cells[t.curY]
		copy(row[t.curX:], row[t.curX+n:])
		for x := t.cols - n; x < t.cols; x++ {
			row[x].ch = ' '
		}
	case '@': // insert chars
		n := n0(1)
		row := t.cells[t.curY]
		copy(row[t.curX+n:], row[t.curX:])
		for x := t.curX; x < t.curX+n && x < t.cols; x++ {
			row[x].ch = ' '
		}
	case 'd': // line position absolute
		t.curY = n0(1) - 1
		if t.curY < 0 {
			t.curY = 0
		}
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case 'G': // cursor horizontal absolute
		t.curX = n0(1) - 1
		if t.curX < 0 {
			t.curX = 0
		}
	case 'S': // scroll up
		for i := 0; i < n0(1); i++ {
			t.scrollUp()
		}
	case 'm', 'h', 'l', 'r', 's', 'u', 'n':
		// SGR / mode — ignore, we don't do color
	}
}

func parseParams(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		nums = append(nums, n)
	}
	return nums
}

// Snapshot returns the current screen as a string for display.
func (t *TermBuffer) Snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sb strings.Builder
	for y, row := range t.cells {
		for _, c := range row {
			if c.ch == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(c.ch)
			}
		}
		if y < len(t.cells)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// TermWidget — focusable widget backed by TermBuffer
type TermWidget struct {
	widget.BaseWidget
	app    *App
	buf    *TermBuffer
	grid   *widget.TextGrid
}

func newTermWidget(a *App, cols, rows int) *TermWidget {
	t := &TermWidget{
		app: a,
		buf: newTermBuffer(cols, rows),
	}
	t.grid = widget.NewTextGrid()
	t.grid.ShowLineNumbers = false
	t.ExtendBaseWidget(t)
	return t
}

func (t *TermWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.grid)
}

func (t *TermWidget) refresh() {
	t.grid.SetText(t.buf.Snapshot())
}

func (t *TermWidget) FocusGained()              {}
func (t *TermWidget) FocusLost()                {}
func (t *TermWidget) AcceptsTab() bool          { return true }
func (t *TermWidget) Tapped(*fyne.PointEvent)   { t.app.win.Canvas().Focus(t) }
func (t *TermWidget) Dragged(*fyne.DragEvent)   {}
func (t *TermWidget) DragEnd()                  {}

func (t *TermWidget) TypedRune(r rune) { t.send(string(r)) }
func (t *TermWidget) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyReturn, fyne.KeyEnter:
		t.send("\r")
	case fyne.KeyBackspace:
		t.send("\x7f")
	case fyne.KeyDelete:
		t.send("\x1b[3~")
	case fyne.KeyUp:
		t.send("\x1b[A")
	case fyne.KeyDown:
		t.send("\x1b[B")
	case fyne.KeyRight:
		t.send("\x1b[C")
	case fyne.KeyLeft:
		t.send("\x1b[D")
	case fyne.KeyTab:
		t.send("\t")
	case fyne.KeyEscape:
		t.send("\x1b")
	case fyne.KeyHome:
		t.send("\x1b[H")
	case fyne.KeyEnd:
		t.send("\x1b[F")
	case fyne.KeyPageUp:
		t.send("\x1b[5~")
	case fyne.KeyPageDown:
		t.send("\x1b[6~")
	}
}

func (t *TermWidget) TypedShortcut(s fyne.Shortcut) {
	sc, ok := s.(*desktop.CustomShortcut)
	if !ok || sc.Modifier != fyne.KeyModifierControl {
		return
	}
	switch sc.KeyName {
	case fyne.KeyC:
		t.send("\x03")
	case fyne.KeyD:
		t.send("\x04")
	case fyne.KeyZ:
		t.send("\x1a")
	case fyne.KeyL:
		t.send("\x0c")
	case fyne.KeyA:
		t.send("\x01")
	case fyne.KeyE:
		t.send("\x05")
	case fyne.KeyU:
		t.send("\x15")
	case fyne.KeyK:
		t.send("\x0b")
	case fyne.KeyW:
		t.send("\x17")
	}
}

func (t *TermWidget) send(s string) {
	t.app.sshMu.Lock()
	stdin := t.app.sshStdin
	t.app.sshMu.Unlock()
	if stdin != nil {
		fmt.Fprint(stdin, s)
	}
}

func (t *TermWidget) DroppedFiles(uris []fyne.URI) {
	for _, uri := range uris {
		t.app.uploadFile(uri.Path())
	}
}

// App holds all application state
type App struct {
	fyneApp    fyne.App
	win        fyne.Window
	termWidget *TermWidget
	sshClient  *ssh.Client
	sshSession *ssh.Session
	sshStdin   io.WriteCloser
	sshMu      sync.Mutex
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
	split      *container.Split
	sideVisible bool
	toggleBtn   *widget.Button
}

const (
	termCols = 220
	termRows = 50
)

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	myApp := &App{
		fyneApp:    a,
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
		a.sshMu.Lock()
		connected := a.sshClient != nil
		a.sshMu.Unlock()
		if connected {
			a.disconnect()
		} else {
			a.connect()
		}
	})
	a.connectBtn.Importance = widget.HighImportance

	uploadBtn := widget.NewButton("⬆ Enviar arquivo", func() {
		a.sshMu.Lock()
		connected := a.sshClient != nil
		a.sshMu.Unlock()
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

	a.termWidget = newTermWidget(a, termCols, termRows)
	termScroll := container.NewScroll(a.termWidget)

	a.toggleBtn = widget.NewButton("◀", func() {
		a.toggleSidebar()
	})
	a.toggleBtn.Importance = widget.LowImportance

	// toggleBtn sits outside the split so it's always visible
	termPanel := container.NewBorder(
		nil,
		a.statusBar,
		nil, nil,
		termScroll,
	)

	a.sideVisible = true
	a.split = container.NewHSplit(sidePanel, termPanel)
	a.split.SetOffset(0.22)

	// outer layout: [toggleBtn | split]
	a.win.SetContent(container.NewBorder(nil, nil, a.toggleBtn, nil, a.split))
	a.win.Canvas().Focus(a.termWidget)
}

func (a *App) toggleSidebar() {
	if a.sideVisible {
		a.split.SetOffset(0)
		a.sideVisible = false
		a.toggleBtn.SetText("▶")
	} else {
		a.split.SetOffset(0.22)
		a.sideVisible = true
		a.toggleBtn.SetText("◀")
	}
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
			User: user, Auth: authMethods,
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

		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
		if err := session.RequestPty("xterm", termRows, termCols, modes); err != nil {
			session.Close(); client.Close()
			a.setStatus(fmt.Sprintf("Erro PTY: %v", err))
			a.connectBtn.Enable()
			return
		}

		stdin, err := session.StdinPipe()
		if err != nil {
			session.Close(); client.Close()
			a.setStatus(fmt.Sprintf("Erro stdin: %v", err))
			a.connectBtn.Enable()
			return
		}

		pr, pw := io.Pipe()
		session.Stdout = pw
		session.Stderr = pw

		if err := session.Shell(); err != nil {
			session.Close(); client.Close()
			a.setStatus(fmt.Sprintf("Erro shell: %v", err))
			a.connectBtn.Enable()
			return
		}

		a.sshMu.Lock()
		a.sshClient = client
		a.sshSession = session
		a.sshStdin = stdin
		a.sshMu.Unlock()

		fmt.Fprintf(stdin, "export PROMPT_COMMAND='echo %s$(pwd)'\n", pwdMarker)

		// read SSH output, feed into TermBuffer, refresh widget
		go func() {
			buf := make([]byte, 4096)
			tick := time.NewTicker(16 * time.Millisecond)
			defer tick.Stop()
			dirty := false
			dataCh := make(chan []byte, 64)

			go func() {
				for {
					n, err := pr.Read(buf)
					if n > 0 {
						tmp := make([]byte, n)
						copy(tmp, buf[:n])
						dataCh <- tmp
					}
					if err != nil {
						close(dataCh)
						return
					}
				}
			}()

			for {
				select {
				case data, ok := <-dataCh:
					if !ok {
						goto done
					}
					a.processOutput(data)
					dirty = true
				case <-tick.C:
					if dirty {
						a.termWidget.refresh()
						dirty = false
					}
				}
			}
		done:
			a.termWidget.refresh()
			a.setStatus("Sessão encerrada")
			a.connectBtn.SetText("Conectar")
			a.connectBtn.Importance = widget.HighImportance
			a.sshMu.Lock()
			a.sshClient = nil
			a.sshSession = nil
			a.sshStdin = nil
			a.sshMu.Unlock()
			a.setPwd("~")
		}()

		a.setStatus(fmt.Sprintf("Conectado — %s@%s", user, addr))
		a.connectBtn.SetText("Desconectar")
		a.connectBtn.Importance = widget.DangerImportance
		a.connectBtn.Enable()
		a.win.Canvas().Focus(a.termWidget)
		// auto-collapse sidebar after connecting
		if a.sideVisible {
			a.toggleSidebar()
		}
	}()
}

func (a *App) processOutput(data []byte) {
	// filter pwdMarker lines out of terminal output, update pwd label
	s := string(data)
	if !strings.Contains(s, pwdMarker) {
		a.termWidget.buf.Write(data)
		return
	}
	lines := strings.Split(s, "\n")
	var keep []string
	for _, line := range lines {
		if idx := strings.Index(line, pwdMarker); idx >= 0 {
			pwd := strings.TrimSpace(line[idx+len(pwdMarker):])
			if pwd != "" {
				a.setPwd(pwd)
			}
		} else {
			keep = append(keep, line)
		}
	}
	filtered := strings.Join(keep, "\n")
	if filtered != "" {
		a.termWidget.buf.Write([]byte(filtered))
	}
}

func (a *App) disconnect() {
	a.sshMu.Lock()
	defer a.sshMu.Unlock()
	if a.sshSession != nil {
		a.sshSession.Close()
	}
	if a.sshClient != nil {
		a.sshClient.Close()
	}
	a.sshClient = nil
	a.sshSession = nil
	a.sshStdin = nil
	a.connectBtn.SetText("Conectar")
	a.connectBtn.Importance = widget.HighImportance
	a.setStatus("Desconectado")
	a.setPwd("~")
}

func (a *App) uploadFile(path string) {
	a.sshMu.Lock()
	client := a.sshClient
	a.sshMu.Unlock()
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
			dialog.ShowError(fmt.Errorf("Falha ao enviar %s:\n%v", filename, err), a.win)
			return
		}
		msg := fmt.Sprintf("✓ %s → %s", filename, remoteDir)
		a.setStatus(msg)
		dialog.ShowInformation("Upload concluído", msg, a.win)
	}()
}
