package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

const pwdMarker = "__SSHTERM_PWD__:"

type App struct {
	ctx        context.Context
	sshClient  *ssh.Client
	sshSession *ssh.Session
	sshStdin   io.WriteCloser
	mu         sync.Mutex
	currentPwd string
}

// Host represents a saved SSH host
type Host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	KeyPath  string `json:"keyPath"`
}

func NewApp() *App {
	return &App{currentPwd: "~"}
}

func hostsFilePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "sshterm")
	os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "hosts.json")
}

// GetHosts returns all saved hosts
func (a *App) GetHosts() []Host {
	data, err := os.ReadFile(hostsFilePath())
	if err != nil {
		return []Host{}
	}
	var hosts []Host
	if err := json.Unmarshal(data, &hosts); err != nil {
		return []Host{}
	}
	return hosts
}

// SaveHost saves or updates a host (matched by ID)
func (a *App) SaveHost(host Host) error {
	hosts := a.GetHosts()
	if host.ID == "" {
		host.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	found := false
	for i, h := range hosts {
		if h.ID == host.ID {
			hosts[i] = host
			found = true
			break
		}
	}
	if !found {
		hosts = append(hosts, host)
	}
	data, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(hostsFilePath(), data, 0600)
}

// DeleteHost removes a host by ID
func (a *App) DeleteHost(id string) error {
	hosts := a.GetHosts()
	filtered := hosts[:0]
	for _, h := range hosts {
		if h.ID != id {
			filtered = append(filtered, h)
		}
	}
	data, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(hostsFilePath(), data, 0600)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sshSession != nil {
		a.sshSession.Close()
	}
	if a.sshClient != nil {
		a.sshClient.Close()
	}
}

// ClipboardGetText returns text from clipboard (fallback for WebView2 clipboard restrictions)
func (a *App) ClipboardGetText() string {
	text, _ := runtime.ClipboardGetText(a.ctx)
	return text
}

// Connect establishes SSH connection and starts shell
func (a *App) Connect(host, port, user, password, keyPath string) error {
	a.mu.Lock()
	if a.sshClient != nil {
		a.mu.Unlock()
		return fmt.Errorf("já conectado")
	}
	a.mu.Unlock()

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
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return err
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 115200,
		ssh.TTY_OP_OSPEED: 115200,
	}
	if err := session.RequestPty("xterm-256color", 50, 220, modes); err != nil {
		session.Close()
		client.Close()
		return err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return err
	}

	pr, pw := io.Pipe()
	session.Stdout = pw
	session.Stderr = pw

	if err := session.Shell(); err != nil {
		session.Close()
		client.Close()
		return err
	}

	a.mu.Lock()
	a.sshClient = client
	a.sshSession = session
	a.sshStdin = stdin
	a.mu.Unlock()

	// Inject PROMPT_COMMAND to track pwd in real-time
	fmt.Fprintf(stdin, "export PROMPT_COMMAND='echo %s$(pwd)'\n", pwdMarker)

	// Stream output to frontend, filtering pwd markers
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				a.processOutput(string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		runtime.EventsEmit(a.ctx, "terminal:closed", nil)
		a.mu.Lock()
		a.sshClient = nil
		a.sshSession = nil
		a.sshStdin = nil
		a.mu.Unlock()
	}()

	return nil
}

func (a *App) processOutput(raw string) {
	if !strings.Contains(raw, pwdMarker) {
		runtime.EventsEmit(a.ctx, "terminal:data", raw)
		return
	}
	lines := strings.Split(raw, "\n")
	var keep []string
	for _, line := range lines {
		if idx := strings.Index(line, pwdMarker); idx >= 0 {
			pwd := strings.TrimSpace(line[idx+len(pwdMarker):])
			if pwd != "" {
				a.mu.Lock()
				a.currentPwd = pwd
				a.mu.Unlock()
				runtime.EventsEmit(a.ctx, "terminal:pwd", pwd)
			}
		} else {
			keep = append(keep, line)
		}
	}
	filtered := strings.Join(keep, "\n")
	if filtered != "" {
		runtime.EventsEmit(a.ctx, "terminal:data", filtered)
	}
}

// SendInput sends raw bytes to SSH stdin
func (a *App) SendInput(data string) {
	a.mu.Lock()
	stdin := a.sshStdin
	a.mu.Unlock()
	if stdin != nil {
		fmt.Fprint(stdin, data)
	}
}

// Resize notifies SSH server of terminal size change
func (a *App) Resize(cols, rows int) {
	a.mu.Lock()
	session := a.sshSession
	a.mu.Unlock()
	if session != nil {
		session.WindowChange(rows, cols)
	}
}

// Disconnect closes SSH connection
func (a *App) Disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sshSession != nil {
		a.sshSession.Close()
	}
	if a.sshClient != nil {
		a.sshClient.Close()
	}
	a.sshClient = nil
	a.sshSession = nil
	a.sshStdin = nil
}

// IsConnected returns connection state
func (a *App) IsConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sshClient != nil
}

// GetPwd returns current remote directory
func (a *App) GetPwd() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.currentPwd
}


// UploadFile sends a single file via SCP to current pwd
func (a *App) UploadFile(localPath string) error {
	a.mu.Lock()
	client := a.sshClient
	pwd := a.currentPwd
	a.mu.Unlock()

	if client == nil {
		return fmt.Errorf("não conectado")
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return a.uploadDir(client, localPath, pwd)
	}
	return a.uploadSingleFile(client, localPath, pwd)
}

func (a *App) uploadDir(client *ssh.Client, localDir, remoteBase string) error {
	dirName := filepath.Base(localDir)
	remoteDir := remoteBase + "/" + dirName

	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	sess.Run(fmt.Sprintf("mkdir -p %q", remoteDir))
	sess.Close()

	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		fullPath := filepath.Join(localDir, entry.Name())
		if entry.IsDir() {
			a.uploadDir(client, fullPath, remoteDir)
		} else {
			if err := a.uploadSingleFile(client, fullPath, remoteDir); err != nil {
				runtime.EventsEmit(a.ctx, "upload:error", fmt.Sprintf("%s: %v", entry.Name(), err))
			}
		}
	}
	return nil
}

func (a *App) uploadSingleFile(client *ssh.Client, path, remoteDir string) error {
	filename := filepath.Base(path)
	runtime.EventsEmit(a.ctx, "upload:progress", fmt.Sprintf("Enviando %s...", filename))

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	scpStdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- session.Run(fmt.Sprintf("scp -t %q", remoteDir)) }()

	fmt.Fprintf(scpStdin, "C0644 %d %s\n", info.Size(), filename)
	io.Copy(scpStdin, f)
	fmt.Fprint(scpStdin, "\x00")
	scpStdin.Close()

	if err := <-errCh; err != nil {
		return err
	}

	runtime.EventsEmit(a.ctx, "upload:done", fmt.Sprintf("✓ %s → %s", filename, remoteDir))
	return nil
}

// OpenFileDialog opens native file picker, returns selected paths
func (a *App) OpenFileDialog() []string {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Selecionar arquivos",
	})
	if err != nil {
		return nil
	}
	return paths
}

// OpenFolderDialog opens native folder picker
func (a *App) OpenFolderDialog() string {
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Selecionar pasta",
	})
	if err != nil {
		return ""
	}
	return path
}

// DragDropFiles is called by the frontend with file paths from a drop event.
// On WebView2, file.path is not available — instead we use the Wails
// OnFileDrop option which passes paths directly from the OS drag event.
func (a *App) DragDropFiles(x, y float64, paths []string) {
	a.UploadPaths(paths)
}

// UploadPaths uploads multiple paths (files or folders)
func (a *App) UploadPaths(paths []string) {
	for _, p := range paths {
		if err := a.UploadFile(p); err != nil {
			runtime.EventsEmit(a.ctx, "upload:error", fmt.Sprintf("%s: %v", filepath.Base(p), err))
		}
	}
	runtime.EventsEmit(a.ctx, "upload:complete", nil)
}