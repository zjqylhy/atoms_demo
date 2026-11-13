ARG PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.64.1-noble@sha256:2f05f8a734509a7b3f26a912639488ee08f58a7b292d14d62271bcff9038768f
FROM ${PLAYWRIGHT_IMAGE} AS go-toolchain
RUN set -eu; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) sha=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840 ;; \
        arm64) sha=3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92 ;; \
        *) exit 1 ;; \
    esac; \
    curl --fail --silent --show-error --location --retry 2 --max-time 120 \
        "https://go.dev/dl/go1.27.4.linux-${arch}.tar.gz" -o /tmp/go.tar.gz; \
    echo "$sha  /tmp/go.tar.gz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm /tmp/go.tar.gz
# Build the common standard library once so a capped job can spend its time on project tests.
RUN CGO_ENABLED=0 GOMAXPROCS=1 GOENV=off GOTOOLCHAIN=local GOCACHE=/opt/atoms/go-cache \
    /usr/local/go/bin/go test -p=1 -run '^$' net testing && du -sh /opt/atoms/go-cache

FROM ${PLAYWRIGHT_IMAGE}
WORKDIR /opt/atoms
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 NODE_PATH=/opt/atoms/node_modules HOME=/tmp
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY --from=go-toolchain /usr/local/go /usr/local/go
COPY --from=go-toolchain /opt/atoms/go-cache /opt/atoms/go-cache
RUN /usr/local/go/bin/go version && /usr/bin/python3 --version
COPY job.cjs outputs.cjs project-path.cjs web.cjs browser.cjs app-host.cjs ./
LABEL org.atoms.preview-runtime="1"
WORKDIR /workspace
ENTRYPOINT ["node", "/opt/atoms/job.cjs"]
