FROM node:22-bookworm-slim

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web .
COPY api/examples /app/api/examples
COPY api/schemas /app/api/schemas
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
