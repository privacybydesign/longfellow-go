# Build environment for longfellow-zk and the Go binding over it.
#
# irmago #724 Phase 0 step 1: "Build longfellow for linux/amd64 and darwin/arm64.
# Confirm `cmake --install` yields libmdoc_static.a + mdoc_zk.h. Write
# scripts/build-longfellow.sh."
#
# A container rather than a developer's shell, for two reasons. #724 Phase 1
# wants CI building this from source, and this is what CI will run. And the
# alternative has already cost us: hopping between WSL and a Windows host on the
# same tree produced two failures whose causes were entirely environmental.
#
# Constraint this exists to satisfy (#724): "No prebuilt binaries may enter the
# repo or the module graph." Everything here is built from source at a pinned
# commit.
FROM ubuntu:24.04

# Exactly the dependency set longfellow-zk's README names for Debian/Ubuntu,
# plus git to fetch it and golang to build the binding against it.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential clang cmake git ca-certificates \
      libssl-dev libzstd-dev libgtest-dev libbenchmark-dev zlib1g-dev \
      golang-go \
    && rm -rf /var/lib/apt/lists/*

# Pinned, not floating. The circuit specs we depend on are version-sensitive:
# v6 is what the AV profile pins and generate_circuit only emits the newest
# version, so "latest" is not a safe default here.
ARG LONGFELLOW_REF=61a8a735964d1b22bccf79bf14ef6767249cdf92
RUN git clone https://github.com/google/longfellow-zk.git /src/longfellow-zk \
    && git -C /src/longfellow-zk checkout --quiet "${LONGFELLOW_REF}"

# patches/ holds changes submitted upstream but not released yet. Each is
# applied here against the pinned ref above, and deleted from this repository
# once a ref containing it is pinned instead.
#
# Applying rather than forking keeps #724's constraint intact: the library is
# still built from upstream's own source at a named commit, and the delta is one
# reviewable file rather than a fork nobody tracks.
#
# A patch that stops applying after a ref bump fails the image build, which is
# the signal to check whether it landed upstream. That is the intended
# behaviour, not an inconvenience to work around.
COPY patches/ /src/patches/
RUN for p in /src/patches/*.patch; do \
      echo "==> applying $(basename "$p")"; \
      git -C /src/longfellow-zk apply --verbose "$p"; \
    done

COPY scripts/ /usr/local/bin/
RUN chmod +x /usr/local/bin/*.sh

WORKDIR /src/longfellow-zk

# Build at image-build time so the install tree is part of the image rather than
# lost with the container. The previous run used `docker run --rm` and threw away
# libmdoc_static.a, which step 2 links against.
#
# This also makes the image the unit of reproducibility: anyone who pulls it has
# the same library from the same pinned commit, with no build step to get wrong.
RUN build-longfellow.sh

# Where the cgo directives in Google's reference binding expect to find things:
#   #cgo LDFLAGS: -L../../install/lib -lmdoc_static -lcrypto -lzstd -lstdc++
#   #cgo CFLAGS:  -I../../install/include
ENV LONGFELLOW_INSTALL=/src/longfellow-zk/install

# A newer Go than Ubuntu 24.04 ships. The distro package is 1.22 and irmago's
# go.mod declares `go 1.27`, which 1.22 refuses outright rather than degrading —
# and this module imports irmago's zk package, so its floor is ours.
#
# Deliberately a layer of its own, after the library build: changing the Go
# version must not invalidate the twenty minutes of C++ above it. Installed from
# the official tarball rather than by GOTOOLCHAIN=auto, so a build does not
# depend on go.dev being reachable at run time.
ARG GO_VERSION=1.27.0
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
    && rm -rf /usr/local/go \
    && tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH=/usr/local/go/bin:$PATH

# What the module's cgo directives deliberately do NOT hardcode. Google's
# reference binding writes -L../../install/lib, which resolves only from its own
# directory in its own checkout; #724 asks for configurable link paths.
ENV CGO_CFLAGS=-I/src/longfellow-zk/install/include
ENV CGO_LDFLAGS=-L/src/longfellow-zk/install/lib
