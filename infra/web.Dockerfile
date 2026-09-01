FROM node:22-alpine
RUN corepack enable
WORKDIR /app
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml* ./
COPY apps/web/package.json apps/web/package.json
RUN pnpm install --frozen-lockfile=false
COPY apps/web apps/web
EXPOSE 5173
CMD ["pnpm", "--filter", "@circular/web", "dev", "--host", "0.0.0.0"]
