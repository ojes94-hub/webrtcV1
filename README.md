# Meet

A browser-based multi-room WebRTC calling application. Calls use a peer-to-peer mesh for up to 10 participants; Go provides static hosting, room-scoped WebSocket signaling, chat/presence relays, and ICE configuration.

## Local development

Requirements: Go 1.24 or newer and a modern browser.

```powershell
go mod download
go run .
```

Open `http://localhost:8080`, enter a display name, create a meeting or join with a room code, and allow camera/microphone access. Localhost is treated as a secure browser context for media access.

Create and verify a production-style binary:

```powershell
go test ./...
go vet ./...
go build -tags netgo -ldflags "-s -w" -o app .
```

## Environment

| Variable | Required | Description |
| --- | --- | --- |
| `PORT` | No | HTTP listen port; defaults to `8080`. Render provides this automatically. |
| `TURN_URLS` | No | Comma-separated TURN/TURNS URLs, e.g. `turn:turn.example.com:3478?transport=udp,turns:turn.example.com:5349?transport=tcp`. |
| `TURN_SHARED_SECRET` | No | Coturn REST shared secret. The API creates time-limited HMAC-SHA1 credentials; the secret itself is never returned. |
| `TURN_CREDENTIAL_TTL` | No | Lifetime in seconds for temporary TURN credentials (60–86400; defaults to `3600`). |
| `TURN_USERNAME` | No | Static TURN username fallback if the TURN provider does not support REST credentials. |
| `TURN_CREDENTIAL` | No | Static TURN password fallback. Prefer temporary credentials where possible. |

`GET /api/ice-servers` returns the public STUN server and any configured TURN server credentials with `Cache-Control: no-store`. Credentials are necessarily visible to the browser using them; use the shared-secret mode for expiring credentials rather than embedding a permanent TURN password in the app.

## Render deployment

1. Push the repository to GitHub and create a Render Web Service using the Docker runtime (or let Render apply [render.yaml](./render.yaml)).
2. Set `TURN_URLS` and `TURN_SHARED_SECRET` in the Render dashboard for reliable media on restrictive mobile/NAT networks. The TURN service must support the coturn REST credential format when using the shared-secret option.
3. Deploy. Render terminates HTTPS; the app serves plain HTTP internally and uses `wss:` from HTTPS pages.

The service exposes `/healthz` for Render health checks. Room membership is held in process memory, so use one Render instance unless a shared signaling store is added. HTTPS signaling does not relay WebRTC media: without TURN, some networks will not establish a direct peer route.

## Features

- Private-by-link rooms with isolated participants, signaling, chat, and presence.
- Name entry and camera/microphone preview with input/output selection.
- Mesh video/audio, active-speaker indication, participant list, and in-room chat.
- Camera/microphone toggles, front/back camera switching, screen sharing, picture-in-picture, and pinned tiles.
- WebSocket ping/pong keepalives, reconnect with backoff, queued ICE candidates, and ICE restart attempts.

Mesh calls send media directly between participants and scale quadratically. Keep rooms small; use an SFU for larger meetings.
