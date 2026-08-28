FROM node:22-slim

WORKDIR /app

COPY package.json pnpm-lock.yaml ./
RUN corepack enable && corepack prepare pnpm@latest --activate && pnpm install --frozen-lockfile

COPY tsconfig.json ./
COPY src ./src

USER node

CMD ["./node_modules/.bin/tsx", "src/worker.ts"]
