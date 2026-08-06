# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.0
ARG INNOEXTRACT_COMMIT=098180a06075c2d68fc27da6ec645b47618babfd
ARG INNOEXTRACT_SHA256=aa6d690296e85f3928139f929ff760dcf710a0d379113e7c70b4da3a391bba92
ARG DEBIAN_MIRROR=http://mirrors.tuna.tsinghua.edu.cn/debian
ARG DEBIAN_SECURITY_MIRROR=http://mirrors.tuna.tsinghua.edu.cn/debian-security

FROM debian:bookworm-slim AS innoextract-builder

ARG INNOEXTRACT_COMMIT
ARG INNOEXTRACT_SHA256
ARG DEBIAN_MIRROR
ARG DEBIAN_SECURITY_MIRROR
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN sed -i \
       -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
       -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
       /etc/apt/sources.list.d/debian.sources
RUN --mount=type=cache,id=ace-bookworm-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=ace-bookworm-apt-cache,target=/var/cache/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       build-essential \
       ca-certificates \
       cmake \
       curl \
       gzip \
       libboost-date-time-dev \
       libboost-filesystem-dev \
       libboost-iostreams-dev \
       libboost-program-options-dev \
       libboost-system-dev \
       liblzma-dev \
    && curl -fsSL --http1.1 --retry 5 --retry-all-errors --retry-delay 2 \
       "https://github.com/dscharrer/innoextract/archive/${INNOEXTRACT_COMMIT}.tar.gz" \
       -o /tmp/innoextract.tar.gz \
    && printf '%s  %s\n' "$INNOEXTRACT_SHA256" /tmp/innoextract.tar.gz | sha256sum -c - \
    && mkdir -p /src/innoextract \
    && tar -xzf /tmp/innoextract.tar.gz -C /src/innoextract --strip-components=1 \
    && cmake \
       -S /src/innoextract \
       -B /tmp/innoextract-build \
       -DCMAKE_BUILD_TYPE=Release \
       -DCMAKE_INSTALL_PREFIX=/out \
       -DBUILD_TESTS=OFF \
       -DUSE_LTO=OFF \
    && cmake --build /tmp/innoextract-build --parallel 2 \
    && cmake --install /tmp/innoextract-build --strip \
    && /out/bin/innoextract --version | grep -Fq 'innoextract 1.10-dev'

FROM golang:${GO_VERSION}-bookworm

ARG INNO_SETUP_VERSION=6.3.3
ARG INNO_SETUP_SHA256=0bcb2a409dea17e305a27a6b09555cabe600e984f88570ab72575cd7e93c95e6
ARG DEBIAN_MIRROR
ARG DEBIAN_SECURITY_MIRROR
ENV DEBIAN_FRONTEND=noninteractive \
    WINEARCH=win64 \
    WINEDEBUG=-all \
    WINEPREFIX=/opt/wine
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY --from=innoextract-builder /out/bin/innoextract /usr/local/bin/innoextract
RUN --mount=type=cache,id=ace-bookworm-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=ace-bookworm-apt-cache,target=/var/cache/apt,sharing=locked \
    sed -i \
       -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
       -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
       /etc/apt/sources.list.d/debian.sources \
    && rm -f /etc/apt/apt.conf.d/docker-clean \
    && dpkg --add-architecture i386 \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       binutils \
       ca-certificates \
       curl \
       file \
       jq \
       libboost-date-time1.74.0 \
       libboost-filesystem1.74.0 \
       libboost-iostreams1.74.0 \
       libboost-program-options1.74.0 \
       libboost-system1.74.0 \
       liblzma5 \
       util-linux \
       wine \
       wine32:i386 \
       wine64 \
       xauth \
       xvfb \
    && curl -fsSL --http1.1 --retry 5 --retry-all-errors --retry-delay 2 \
       "https://github.com/jrsoftware/issrc/releases/download/is-${INNO_SETUP_VERSION//./_}/innosetup-${INNO_SETUP_VERSION}.exe" \
       -o /tmp/innosetup.exe \
    && printf '%s  %s\n' "$INNO_SETUP_SHA256" /tmp/innosetup.exe | sha256sum -c - \
    && mkdir -p "$WINEPREFIX" \
    && xvfb-run -a wineboot --init \
    && xvfb-run -a wine /tmp/innosetup.exe \
       /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /DIR=C:\\InnoSetup \
    && wineserver -w \
    && test -f "$WINEPREFIX/drive_c/InnoSetup/ISCC.exe" \
    && rm -f /tmp/innosetup.exe

RUN ! ldd /usr/local/bin/innoextract | grep -Fq 'not found' \
    && innoextract --version | grep -Fq 'innoextract 1.10-dev'

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://goproxy.cn,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY agent ./agent
COPY internal ./internal
COPY tools ./tools
COPY installer ./installer
COPY scripts/build-windows-agent.sh scripts/publish-windows-release.sh ./scripts/

RUN go build -trimpath -ldflags='-s -w' -o /usr/local/bin/ace-release ./tools/cmd/ace-release

RUN <<'EOF'
cat >/usr/local/bin/iscc-wine <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

arguments=()
for argument in "$@"; do
  case "$argument" in
    /DSourceExe=*|/DOutputDir=*)
      prefix="${argument%%=*}="
      path="${argument#*=}"
      arguments+=("${prefix}$(winepath -w "$path")")
      ;;
    *.iss)
      arguments+=("$(winepath -w "$argument")")
      ;;
    *)
      arguments+=("$argument")
      ;;
  esac
done
exec xvfb-run -a wine /opt/wine/drive_c/InnoSetup/ISCC.exe "${arguments[@]}"
SCRIPT
chmod 0755 /usr/local/bin/iscc-wine

cat >/usr/local/bin/build-windows-release <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

version="${RELEASE_VERSION:-}"
commit="${RELEASE_COMMIT:-}"
built_at="${RELEASE_BUILT_AT:-$(date -u +%FT%TZ)}"
published_at="${RELEASE_PUBLISHED_AT:-$built_at}"
minimum_os="${ACE_WINDOWS_MINIMUM_OS:-10.0.17763}"
private_key=/run/secrets/update-signing.key

[[ -n "$version" ]] || fail "RELEASE_VERSION is required"
[[ -n "$commit" ]] || fail "RELEASE_COMMIT is required"
[[ -f "$private_key" && ! -L "$private_key" ]] || fail "update signing key is unavailable"
[[ -r "$private_key" ]] || fail "update signing key is not readable"
[[ -d /out && ! -L /out ]] || fail "/out must be a mounted directory"

work_root="$(mktemp -d /tmp/ace-windows-release.XXXXXX)"
trap 'rm -rf "$work_root"' EXIT
public_key_file="$work_root/update-signing.key.pub"
build_root="$work_root/build"
manifest="$work_root/latest.json"

public_key="$(ace-release public-key -private "$private_key")"
printf '%s\n' "$public_key" >"$public_key_file"
chmod 0600 "$public_key_file"

ACE_UPDATE_PUBLIC_KEY="$public_key" \
ISCC=/usr/local/bin/iscc-wine \
scripts/build-windows-agent.sh "$version" "$commit" "$built_at" "$build_root"
unset public_key

artifact="$build_root/AceAgentSetup-windows-amd64-V${version}.exe"
artifact_url="/downloads/windows/stable/AceAgentSetup-windows-amd64-V${version}.exe"
ace-release sign \
  -private "$private_key" \
  -artifact "$artifact" \
  -manifest "$manifest" \
  -version "$version" \
  -published-at "$published_at" \
  -minimum-os "$minimum_os" \
  -url "$artifact_url"
ace-release verify \
  -public "$public_key_file" \
  -artifact "$artifact" \
  -manifest "$manifest" >/dev/null

inventory="$work_root/installer-inventory.txt"
innoextract --list "$artifact" >"$inventory"
grep -Fiq 'AceAgent.exe' "$inventory" \
  || fail "installer inventory does not contain AceAgent.exe"
file "$artifact" | grep -Fq 'PE32' || fail "installer is not a Windows PE executable"

upgrade_test_dir="$WINEPREFIX/drive_c/AceUpgradeContract"
upgrade_config_dir="$WINEPREFIX/drive_c/ProgramData/AceITCenter"
upgrade_config="$upgrade_config_dir/agent.json"
rm -rf "$upgrade_test_dir" "$upgrade_config_dir"
mkdir -p "$upgrade_test_dir" "$upgrade_config_dir"
printf 'old-agent\n' >"$upgrade_test_dir/AceAgent.exe"
printf '{"server_url":"https://preserved.example","credential":"preserved"}\n' >"$upgrade_config"
cp "$upgrade_config" "$work_root/agent.json.before"
xvfb-run -a wine "$artifact" \
  /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /UPDATEHELPER \
  '/DIR=C:\AceUpgradeContract'
wineserver -w
cmp "$build_root/AceAgent.exe" "$upgrade_test_dir/AceAgent.exe" \
  || fail "installer did not replace the existing Agent executable"
cmp "$work_root/agent.json.before" "$upgrade_config" \
  || fail "installer changed the existing Agent configuration"

scripts/publish-windows-release.sh \
  /out "$artifact" "$manifest" "$public_key_file"
SCRIPT
chmod 0755 /usr/local/bin/build-windows-release
EOF

RUN wine reg add "HKCU\Software\Wine" /v Version /t REG_SZ /d win10 /f \
    && wineserver -w
ENTRYPOINT ["/usr/local/bin/build-windows-release"]
