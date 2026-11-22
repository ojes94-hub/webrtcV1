# Meet

A browser-based multi-room video calling application. Small rooms use peer-to-peer WebRTC; larger rooms can use a LiveKit SFU. Go provides static hosting, room-scoped WebSocket signaling, chat/presence relays, and short-lived ICE and LiveKit credentials.

## Local development

Requirements: Go 1.24 or newer and a modern browser.

```powershell
go mod download
go run .
```

Open `http://localhost:8080`, enter a display name, choose **New 1-on-1 call** or **New conference**, and allow camera/microphone access. Conference mode requires LiveKit environment variables; peer-to-peer mode works without them. Localhost is treated as a secure browser context for media access.

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
| `LIVEKIT_URL` | For conferences | LiveKit server URL, for example `wss://your-project.livekit.cloud`. |
| `LIVEKIT_API_KEY` | For conferences | LiveKit project API key. |
| `LIVEKIT_API_SECRET` | For conferences | LiveKit API secret; used only by the server to sign short-lived participant tokens. |

`GET /api/ice-servers` returns the public STUN server and any configured TURN server credentials with `Cache-Control: no-store`. Credentials are necessarily visible to the browser using them; use the shared-secret mode for expiring credentials rather than embedding a permanent TURN password in the app.

`POST /api/conference-token` accepts `{ "room": "...", "username": "..." }` and returns a short-lived signed token and LiveKit URL. The API secret is never sent to browsers. Configure all three `LIVEKIT_*` variables to enable conference rooms. Invite links retain the selected call mode.

## Render deployment

1. Push the repository to GitHub and create a Render Web Service using the Docker runtime (or let Render apply [render.yaml](./render.yaml)).
2. Set `TURN_URLS` and `TURN_SHARED_SECRET` for reliable peer-to-peer media on restrictive mobile/NAT networks. For conference rooms, also set `LIVEKIT_URL`, `LIVEKIT_API_KEY`, and `LIVEKIT_API_SECRET` in the Render dashboard.
3. Deploy. Render terminates HTTPS; the app serves plain HTTP internally and uses `wss:` from HTTPS pages.

The service exposes `/healthz` for Render health checks. Room membership is held in process memory, so use one Render instance unless a shared signaling store is added. HTTPS signaling does not relay WebRTC media: without TURN, some networks will not establish a direct peer route.

## Features

- Private-by-link rooms with isolated participants, signaling, chat, and presence.
- Name entry and camera/microphone preview with input/output selection.
- Mesh video/audio, active-speaker indication, participant list, and in-room chat.
- Camera/microphone toggles, front/back camera switching, screen sharing, picture-in-picture, and pinned tiles.
- WebSocket ping/pong keepalives, reconnect with backoff, queued ICE candidates, and ICE restart attempts.
- LiveKit conference mode with adaptive stream, simulcast camera publishing, data-channel chat, participant state, screen sharing, and graceful receive-only joins.

Mesh calls send media directly between participants and scale quadratically. Keep rooms small; use an SFU for larger meetings.
