FROM mcr.microsoft.com/playwright:v1.64.1-noble@sha256:2f05f8a734509a7b3f26a912639488ee08f58a7b292d14d62271bcff9038768f
WORKDIR /opt/atoms
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 NODE_PATH=/opt/atoms/node_modules HOME=/tmp
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY job.cjs ./
WORKDIR /workspace
ENTRYPOINT ["node", "/opt/atoms/job.cjs"]
