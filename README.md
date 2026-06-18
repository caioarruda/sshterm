# sshterm

Terminal SSH com drag-and-drop para SCP. Construído com Go + Wails + xterm.js.

## Download

Baixe o `.exe` nos [Releases](../../releases) — requer Windows 10 21H2+ ou Windows 11 (WebView2 já incluso).

## Dev local

```bash
# instalar Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.1

# rodar em modo dev (hot reload)
wails dev

# build de produção
wails build
# gera build/bin/sshterm.exe
```

## Release

```bash
git tag v1.0.0
git push origin v1.0.0
```

O Actions builda no `windows-latest` e publica o `.zip` no release.

## Features

- Terminal xterm.js completo (cores, setas, tab completion, histórico)
- DnD de arquivos/pastas do Explorer direto no terminal
- Upload de múltiplos arquivos e pastas recursivas via SCP
- Sidebar recolhível, auto-recolhe ao conectar
- Autenticação por senha ou chave SSH
- Auto-detect de `~/.ssh/id_rsa`, `id_ed25519`, `id_ecdsa`
- Diretório remoto atualizado automaticamente
