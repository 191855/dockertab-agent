# DockerTab Agent

Lightweight backend for the DockerTab iOS app. Runs on your server and exposes a REST + WebSocket API for monitoring and managing Docker containers from your phone.

No cloud, no account. Your phone and your server on the same network.

---

## Features

- View all running and stopped containers
- Live CPU, memory, and network stats streamed over WebSocket
- Real-time log tailing, same as `docker logs -f`
- Start, stop, and restart containers from your phone
- Run one agent per host — manage all of them from a single app

---

## Quick Start

Create a `docker-compose.yml` on your server:

```yaml
services:
  dockertab-agent:
    image: 191855/dockertab-agent:latest
    container_name: dockertab-agent
    restart: unless-stopped
    ports:
      - "8377:8377"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - dockertab-config:/root/.config/dockertab
    environment:
      - DOCKERTAB_HOST=192.168.1.50   # your server's LAN IP — required
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
  191855/dockertab-agent:latest
```

---

## Pairing

Start the agent — a QR code appears in the logs. Open the DockerTab app, tap **Add Server**, and scan it. The app trades the API key for a JWT token and stores it in the iOS Keychain.

If you can't scan the code, grab the API key from the logs and call the pair endpoint directly:

```bash
curl -X POST http://192.168.1.50:8377/api/v1/pair \
  -H "Content-Type: application/json" \
  -d '{"api_key": "YOUR_KEY", "device_id": "my-iphone", "device_name": "My iPhone"}'
```

---

## Configuration

All options are environment variables. A JSON config file at `~/.config/dockertab/dockertab-agent.json` is also supported, but environment variables always take precedence.

| Variable | Default | Description |
|---|---|---|
| `DOCKERTAB_HOST` | _(none)_ | Your server's LAN IP or hostname. Required — without it the QR code contains `0.0.0.0`, which won't connect. |
| `DOCKERTAB_NAME` | _(none)_ | Friendly name shown in the iOS app (e.g. `Home NAS`). |
| `DOCKERTAB_PORT` | `8377` | Port to listen on. |
| `DOCKERTAB_BIND` | `0.0.0.0` | Bind address. |
| `DOCKERTAB_API_KEY` | _(auto)_ | Pairing API key. Auto-generated on first run. Pin this if you want a stable key without a config volume. |
| `DOCKERTAB_JWT_SECRET` | _(auto)_ | JWT signing secret. Same as above. |
| `DOCKERTAB_DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket path. |
| `DOCKERTAB_LOG_LEVEL` | `info` | Set to `debug` for verbose output. |

### Relay (Remote Access)

To access your server from outside your home network, you can route connections through a DockerTab Relay:

| Variable | Description |
|---|---|
| `DOCKERTAB_RELAY_URL` | WebSocket URL of your relay (e.g. `wss://relay.example.com:8443`) |
| `DOCKERTAB_RELAY_TOKEN` | Token issued by the relay's `/api/v1/register` endpoint |

### Push Notifications (APNs)

When using a relay, push notifications are sent by the relay server — no APNs config needed on the agent. For LAN-only setups without a relay, you can configure APNs directly on the agent:

| Variable | Description |
|---|---|
| `DOCKERTAB_APNS_KEY_FILE` | Path to your Apple `.p8` private key |
| `DOCKERTAB_APNS_KEY_ID` | 10-character key ID (Apple Developer portal → Keys) |
| `DOCKERTAB_APNS_TEAM_ID` | 10-character team ID (Apple Developer portal → Membership) |
| `DOCKERTAB_APNS_SANDBOX` | `true` for development / TestFlight builds |

---

## Multi-Host Setup

Deploy one agent per server. Each generates its own QR code. Scan each one from the app — they show up as separate entries in the server list.

```yaml
# Server 1
environment:
  - DOCKERTAB_HOST=192.168.1.50
  - DOCKERTAB_NAME=Home NAS

# Server 2
environment:
  - DOCKERTAB_HOST=192.168.1.51
  - DOCKERTAB_NAME=Dev Box
```

---

## Security

The agent sits between your Docker socket and the network:

- The API only covers list, inspect, start, stop, restart, stats, and logs — nothing like exec, pull, or volume management.
- The Docker socket is mounted read-only inside the container.
- All endpoints except the health check and pairing require a valid JWT. Tokens are valid for 30 days.
- API keys and JWT secrets are generated with `crypto/rand` and stored at `0600` permissions.
- The pairing API key only appears in the terminal — it's never sent over HTTP.

For production, put the agent behind a reverse proxy (Caddy or Traefik) with TLS. The agent itself serves plain HTTP on port 8377.

---

## Persisting Secrets

On first start the agent writes a random API key and JWT secret to disk. With the `dockertab-config` volume mounted, these survive container rebuilds. Without the volume they regenerate on every rebuild, which breaks existing pairings.

To skip the volume, set `DOCKERTAB_API_KEY` and `DOCKERTAB_JWT_SECRET` to fixed values in your compose file.

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
| `GET` | `/api/v1/containers/:id/logs/stream` | WebSocket: live log stream |
| `GET` | `/api/v1/containers/:id/stats/stream` | WebSocket: live stats (2s interval) |

---

## Troubleshooting

**QR code shows `0.0.0.0`** — set `DOCKERTAB_HOST` to your server's LAN IP.

**App can't connect after scanning** — make sure your phone and server are on the same network and port 8377 isn't blocked by a firewall.

**`permission denied` on Docker socket** — if you're running outside Docker Compose, add your user to the `docker` group or check the socket path.

**Pairing fails after a rebuild** — without a config volume, the API key regenerates on every rebuild. Either mount `dockertab-config` or set `DOCKERTAB_API_KEY` to a fixed value.

**WebSocket drops immediately** — if you're behind nginx, add `proxy_set_header Upgrade $http_upgrade` and `proxy_set_header Connection "upgrade"`.

---

## License

Proprietary — DockerTab 2025
