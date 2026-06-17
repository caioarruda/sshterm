FROM golang:1.22-bullseye

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        gcc-mingw-w64-x86-64 \
        libz-mingw-w64-dev \
        zip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV GOOS=windows \
    GOARCH=amd64 \
    CGO_ENABLED=1 \
    CC=x86_64-w64-mingw32-gcc \
    CGO_LDFLAGS="-static -lgdi32 -lopengl32 -lwinmm" \
    FYNE_SCALE=1

RUN go build \
    -ldflags="-H windowsgui -s -w" \
    -o sshterm.exe \
    . && \
    zip sshterm-windows-amd64.zip sshterm.exe

CMD ["cp", "sshterm-windows-amd64.zip", "/out/sshterm-windows-amd64.zip"]
