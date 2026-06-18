package main

import (
	"context"
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

type App struct {
	ctx        context.Context
	sshClient  *ssh.Client
	sshSession *ssh.Session
	sshStdin   io.WriteCloser
	mu         sync.Mutex
	currentPwd string
}

func NewApp() *App {
	return &App{currentPwd: "~"}
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

	// Stream output to frontend
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				data := string(buf[:n])
				runtime.EventsEmit(a.ctx, "terminal:data", data)
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

	// Track pwd via side channel
	go a.trackPwd(client)

	return nil
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

func (a *App) trackPwd(client *ssh.Client) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.mu.Lock()
		alive := a.sshClient == client
		a.mu.Unlock()
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
			a.mu.Lock()
			a.currentPwd = pwd
			a.mu.Unlock()
			runtime.EventsEmit(a.ctx, "terminal:pwd", pwd)
		}
	}
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

// UploadPaths uploads multiple paths (files or folders)
func (a *App) UploadPaths(paths []string) {
	for _, p := range paths {
		if err := a.UploadFile(p); err != nil {
			runtime.EventsEmit(a.ctx, "upload:error", fmt.Sprintf("%s: %v", filepath.Base(p), err))
		}
	}
	runtime.EventsEmit(a.ctx, "upload:complete", nil)
}
