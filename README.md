# sshterm

Terminal SSH com drag-and-drop para SCP.

## Dependências

```bash
# Ubuntu/Debian
sudo apt install gcc libgl1-mesa-dev xorg-dev

# macOS (apenas Go + Xcode Command Line Tools)
# Windows: precisa do TDM-GCC ou MSYS2
```

## Build

```bash
go mod tidy
go build -o sshterm .

# Windows
go build -ldflags="-H windowsgui" -o sshterm.exe .
```

## Uso

1. Preencha host, porta (padrão 22), usuário e autenticação (senha ou chave SSH)
2. Clique **Conectar** — abre shell interativo
3. Use o campo de input + Enter para enviar comandos
4. **Drag-and-drop** de arquivos na área cinza → sobe via SCP para o diretório configurado
5. Clique na área de drop para abrir file picker se preferir

## Notas

- Suporte a `~/.ssh/id_rsa`, `id_ed25519`, `id_ecdsa` automático se nenhuma chave for selecionada
- O diretório remoto padrão é `~`; altere o campo "Destino SCP" antes de dropar
- ANSI escape codes são stripped do terminal (limitação do widget TextGrid do Fyne)
- `HostKeyCallback` usa `InsecureIgnoreHostKey` — ok para uso interno, ajuste para produção

## Dependências Go

```
fyne.io/fyne/v2 v2.5.2
golang.org/x/crypto v0.27.0
```
