# sshterm

Terminal SSH com drag-and-drop para SCP. Construído com Go + Fyne.

## Download

Baixe o executável direto nos [Releases](../../releases) do repositório — nenhuma instalação necessária no Windows.

## Build local

### Linux (nativo)

```bash
sudo apt install gcc libgl1-mesa-dev xorg-dev
go mod tidy
go build -o sshterm .
```

### macOS

Requer Xcode Command Line Tools. Sem dependências extras.

```bash
go mod tidy
go build -o sshterm .
```

### Windows (cross-compile via Docker)

```bash
docker build -f Dockerfile.windows -t sshterm-builder .
mkdir dist
docker run --rm -v $(pwd)/dist:/out sshterm-builder
# dist/sshterm-windows-amd64.zip
```

Ou direto no Linux com mingw:

```bash
sudo apt install gcc-mingw-w64-x86-64 libz-mingw-w64-dev
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
  CC=x86_64-w64-mingw32-gcc \
  CGO_LDFLAGS="-static -lgdi32 -lopengl32 -lwinmm" \
  go build -ldflags="-H windowsgui -s -w" -o sshterm.exe .
```

## Release automático (GitHub Actions)

Push de uma tag semântica gera release com o `.zip` do Windows como asset:

```bash
git tag v1.0.0
git push origin v1.0.0
```

Tags com sufixo `-alpha`, `-beta` ou `-rc` são publicadas como pre-release.

## Uso

1. Preencha **host**, **porta** (padrão 22), **usuário** e autenticação (senha ou chave SSH)
2. Clique **Conectar** — abre shell interativo com PTY
3. Digite comandos no campo inferior + Enter para enviar
4. Arraste arquivos para a área de drop → envia via SCP para o diretório configurado
5. Clique na área de drop para abrir o file picker

## Notas

- Se nenhuma chave for selecionada, tenta `~/.ssh/id_rsa`, `id_ed25519` e `id_ecdsa` automaticamente
- O campo **Destino SCP** aceita caminhos absolutos ou `~`; altere antes de dropar
- ANSI escape codes são stripped (limitação do `widget.TextGrid` do Fyne)
- `HostKeyCallback` usa `InsecureIgnoreHostKey` — adequado para rede interna; substitua por `knownhosts.New` para produção
- Se o `.exe` reclamar de DLL faltando, adicione `-lpthread` no `CGO_LDFLAGS`

## Dependências

```
fyne.io/fyne/v2 v2.5.2
golang.org/x/crypto v0.27.0
```
