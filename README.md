# DockerTab Agent

Lightweight backend for the DockerTab iOS app. Runs on your server and exposes a REST + WebSocket API for monitoring and managing Docker containers from your phone.

No account required. Your phone and your server.

---

## Features

- View all running and stopped containers
- Live CPU, memory, and network stats streamed over WebSocket
- Realtime log tailing, same as `docker logs -f`
- Start, stop, and restart containers from your phone
- Run one agent per host, manage all of them from a single app

---

## Quick Start

Clone the repo and run:

```bash
git clone https://github.com/191855/dockertab-agent
cd dockertab-agent
make up    # detects your LAN IP, starts the agent
make logs  # QR code prints here
```

Or without cloning, create a `docker-compose.yml` on your server:

```yaml
services:
  dockertab-agent:
    image: ghcr.io/191855/dockertab-agent:latest
    container_name: dockertab-agent
    restart: unless-stopped
    ports:
      - "8377:8377"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - dockertab-config:/root/.config/dockertab
    environment:
      - DOCKERTAB_HOST=192.168.1.50   # your server's LAN IP required
      - DOCKERTAB_NAME=Home Server    # shows up in the iOS app

volumes:
  dockertab-config:
```

Then start it:

```bash
docker compose up -d
docker compose logs -f   # QR code prints here
```

**Or with a single `docker run`:**

```bash
docker run -d \
  --name dockertab-agent \
  -p 8377:8377 \
  -e DOCKERTAB_HOST=192.168.1.50 \
  -e DOCKERTAB_NAME="Home Server" \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v dockertab-config:/root/.config/dockertab \
  --restart unless-stopped \
  ghcr.io/191855/dockertab-agent:latest
```

---

## Pairing

Start the agent. A QR code appears in the logs. Open the DockerTab app, tap **Add Server**, and scan it. The app exchanges the API key for a JWT and stores it in the iOS Keychain.


---

## Configuration

Two variables are all most setups need: `DOCKERTAB_HOST` (your server's LAN IP, required for the QR code to work) and `DOCKERTAB_NAME` (friendly label shown in the app). Everything else is auto-configured on first run: API key, JWT secret, and port.


---

## Security

- All endpoints except `/healthz` and `/api/v1/pair` require a valid JWT. Tokens expire after 180 days.
- API keys and JWT secrets are generated with `crypto/rand` on first run and stored at `0600`.
- The pairing API key is only accessible via the QR code or the local config file, never returned in HTTP responses.
- The Docker socket is mounted read-only inside the container.
- Environment variable values matching common secret patterns (PASSWORD, TOKEN, KEY, etc.) are redacted in the `/env` endpoint response.

---

## Keeping Secrets Across Rebuilds

On first start the agent generates a random API key and JWT secret and writes them to disk. Mount the `dockertab-config` volume and they'll survive rebuilds. Without it, they regenerate on every rebuild and break existing pairings.

If you'd rather skip the volume, just set `DOCKERTAB_API_KEY` and `DOCKERTAB_JWT_SECRET` to fixed values in your compose file.

---

## API Reference

### Public

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Liveness check |
| `POST` | `/api/v1/pair` | Exchange API key for a JWT |

### Protected (`Authorization: Bearer <token>`)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/host` | Host system info |
| `GET` | `/api/v1/containers` | List all containers |
| `GET` | `/api/v1/containers/:id` | Single container details |
| `POST` | `/api/v1/containers/:id/start` | Start a container |
| `POST` | `/api/v1/containers/:id/stop` | Stop a container |
| `POST` | `/api/v1/containers/:id/restart` | Restart a container |
| `GET` | `/api/v1/containers/:id/stats` | One-shot stats snapshot |
| `GET` | `/api/v1/containers/:id/logs?lines=100` | Last N log lines (max 5000) |
| `GET` | `/api/v1/containers/:id/env` | Environment variables (sensitive values redacted) |
| `GET` | `/api/v1/containers/:id/logs/stream` | WebSocket: live log stream |
| `GET` | `/api/v1/containers/:id/stats/stream` | WebSocket: live stats (2s interval) |
| `GET` | `/api/v1/containers/:id/exec` | WebSocket: interactive shell |
| `GET` | `/api/v1/images` | List images |
| `GET` | `/api/v1/volumes` | List volumes |
| `POST` | `/api/v1/notifications/register` | Register APNs device token |
| `DELETE` | `/api/v1/notifications/unregister` | Unregister APNs device token |

---

## Push Notifications & Remote Access

Push notifications and remote access (outside your home network) are **DockerTab Premium** features, powered by the DockerTab relay — operated by DockerTab.

The agent maintains a persistent WebSocket connection to the relay. This serves two purposes:

- **Push notifications** — container start, stop, and restart events are forwarded to APNs and delivered to your iPhone, even when you're away from home.
- **Remote access** — when your phone is outside your LAN, the relay proxies API requests to your agent so you can manage containers from anywhere without exposing your server to the internet.

**Privacy:** The relay is stateless. It forwards requests and notification payloads without storing or retaining any data. Only the minimum information needed to deliver a notification (container name, action, agent ID) is ever transmitted — no logs, no environment variables, no sensitive data.

---

## Troubleshooting

**QR code shows `0.0.0.0`**: set `DOCKERTAB_HOST` to your server's LAN IP.

**App can't connect after scanning**: make sure your phone and server are on the same network and port 8377 isn't blocked by a firewall.

**`permission denied` on Docker socket**: if you're running outside Docker Compose, add your user to the `docker` group or check the socket path.

**Pairing fails after a rebuild**: without a config volume, the API key regenerates on every rebuild. Either mount `dockertab-config` or set `DOCKERTAB_API_KEY` to a fixed value.

**WebSocket drops immediately**: if you're behind nginx, add `proxy_set_header Upgrade $http_upgrade` and `proxy_set_header Connection "upgrade"`.

---

## License

MIT. See [LICENSE](LICENSE)
