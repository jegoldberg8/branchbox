# branchbox base image.
#
# One image serves every project: the per-project toolchain is installed at
# container start by mise, reading the target repository's own mise.toml. That
# keeps a Go project and a Node project on the same image without a rebuild,
# and keeps the toolchain identical to what the project's CI pins.
#
# Only things that are painful to add per container are baked in here.
#
# trixie rather than bookworm: the jcode release binaries are built against
# glibc 2.38+, and bookworm ships 2.36, so jcode will not even report its
# version there. The `jcode version` call at the end of that layer is what
# turns a broken base into a failed build rather than a puzzling runtime error.
FROM debian:trixie-slim

ARG DEBIAN_FRONTEND=noninteractive
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg jq less openssh-client procps \
      ripgrep tmux unzip xz-utils \
      # mise builds some tools from source; without a compiler those installs
      # fail in a way that is confusing from inside a container.
      build-essential pkg-config \
      # Postgres and ClickHouse clients, for poking at a profile's infra from
      # the same shell that runs the services.
      postgresql-client \
      socat \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI: pull requests and reviews from inside the branch container.
RUN install -d -m 0755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=${TARGETARCH} signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
         > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# A non-root user whose uid matches the host developer, so files created in
# the bind-mounted worktree are owned by them and not by root.
#
# The gid is often one that already exists in the image (macOS puts developers
# in gid 20, which Debian already uses), and macOS uids sit below Debian's
# UID_MIN, so the account is created tolerantly and then referred to by number.
ARG USER_UID=1000
ARG USER_GID=1000
RUN groupadd -g ${USER_GID} devgrp 2>/dev/null || true \
    && useradd --non-unique --no-user-group -m -u ${USER_UID} -g ${USER_GID} -s /bin/bash dev \
    && install -d -o ${USER_UID} -g ${USER_GID} /var/log/branchbox

# mise: per-project toolchains, installed at container start from the target
# repository's mise.toml.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh \
    && chmod 0755 /usr/local/bin/mise

# process-compose: an attached TUI over a stack's services, for when a set of
# tmux windows is harder to read than one dashboard. Installed from the release
# archive because Debian does not package it.
# Pinned to match the host, so a config that works outside works inside.
ARG PROCESS_COMPOSE_VERSION=1.122.0
RUN set -eux; \
    case "${TARGETARCH}" in \
      arm64) asset=process-compose_linux_arm64.tar.gz ;; \
      amd64) asset=process-compose_linux_amd64.tar.gz ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/F1bonacc1/process-compose/releases/download/v${PROCESS_COMPOSE_VERSION}/${asset}" \
      -o /tmp/pc.tar.gz; \
    tar -xzf /tmp/pc.tar.gz -C /tmp process-compose; \
    install -m 0755 /tmp/process-compose /usr/local/bin/process-compose; \
    rm -f /tmp/pc.tar.gz /tmp/process-compose; \
    process-compose version >/dev/null

# direnv: projects that carry a .envrc get it honoured in the container shell.
RUN apt-get update && apt-get install -y --no-install-recommends direnv \
    && rm -rf /var/lib/apt/lists/*

# jcode. The version is pinned by the caller to match the host server, because
# the container binary attaches to the host's socket and the two must speak the
# same protocol.
ARG JCODE_VERSION=latest
RUN set -eux; \
    case "${TARGETARCH}" in \
      arm64) asset=jcode-linux-aarch64.tar.gz ;; \
      amd64) asset=jcode-linux-x86_64.tar.gz ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    if [ "${JCODE_VERSION}" = "latest" ]; then \
      url="https://github.com/1jehuang/jcode/releases/latest/download/${asset}"; \
    else \
      url="https://github.com/1jehuang/jcode/releases/download/v${JCODE_VERSION}/${asset}"; \
    fi; \
    curl -fsSL "$url" -o /tmp/jcode.tar.gz; \
    mkdir -p /tmp/jcode-unpack; \
    tar -xzf /tmp/jcode.tar.gz -C /tmp/jcode-unpack; \
    # The archive holds a single binary named after the platform, not "jcode",
    # so install whatever executable it contains under the expected name.
    bin="$(find /tmp/jcode-unpack -maxdepth 2 -type f -perm -u+x | head -1)"; \
    test -n "$bin"; \
    install -m 0755 "$bin" /usr/local/bin/jcode; \
    rm -rf /tmp/jcode.tar.gz /tmp/jcode-unpack; \
    jcode version >/dev/null

# mise and direnv activate per shell; keeping this in /etc/profile.d means it
# applies to both `bash -l` and tmux windows.
RUN printf '%s\n' \
      'eval "$(mise activate bash)"' \
      'eval "$(direnv hook bash)"' \
      'export PATH="$MISE_DATA_DIR/shims:$PATH"' \
      > /etc/profile.d/branchbox.sh \
    && printf '%s\n' '[ -f /etc/profile.d/branchbox.sh ] && . /etc/profile.d/branchbox.sh' \
      >> /home/dev/.bashrc \
    && chown ${USER_UID}:${USER_GID} /home/dev/.bashrc \
    # Created here so the named volumes branchbox mounts over them inherit the
    # developer's ownership instead of being root-owned and unwritable.
    && install -d -o ${USER_UID} -g ${USER_GID} \
         /home/dev/.cache /home/dev/.config /opt/branchbox/mise

USER dev
# mise state lives outside $HOME: branchbox mounts per-branch cache volumes
# into the home directory, and a volume mounted at ~/.cache hides any sibling
# directory the image created under ~/.local, leaving mise unable to write its
# trust and tracking state.
WORKDIR /
# MISE_TRUSTED_CONFIG_PATHS is set per container by branchbox, since the
# checkout is mounted at its host path and so differs per project.
ENV MISE_DATA_DIR=/opt/branchbox/mise/data \
    MISE_STATE_DIR=/opt/branchbox/mise/state \
    MISE_CACHE_DIR=/opt/branchbox/mise/cache \
    SHELL=/bin/bash
CMD ["sleep", "infinity"]
