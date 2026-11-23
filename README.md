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
| `TURN_URL` | No | Single TURN/TURNS URL alias for native clients; used when `TURN_URLS` is unset. |
| `TURN_SHARED_SECRET` | No | Coturn REST shared secret. The API creates time-limited HMAC-SHA1 credentials; the secret itself is never returned. |
| `TURN_CREDENTIAL_TTL` | No | Lifetime in seconds for temporary TURN credentials (60–86400; defaults to `3600`). |
| `TURN_USERNAME` | No | Static TURN username fallback if the TURN provider does not support REST credentials. |
| `TURN_CREDENTIAL` | No | Static TURN password fallback. Prefer temporary credentials where possible. |
| `LIVEKIT_URL` | For conferences | LiveKit server URL, for example `wss://your-project.livekit.cloud`. |
| `LIVEKIT_API_KEY` | For conferences | LiveKit project API key. |
| `LIVEKIT_API_SECRET` | For conferences | LiveKit API secret; used only by the server to sign short-lived participant tokens. |
| `ROOM_ACCESS_SECRET` | No | Enables opt-in protected rooms. Keep this secret stable across deploys/restarts. Unset means every room behaves normally. |
| `CONFERENCE_TOKEN_RATE_LIMIT` | No | Conference-token requests allowed per IP per window; defaults to `60`. |
| `CONFERENCE_TOKEN_RATE_WINDOW_SECONDS` | No | Conference-token rate-limit window in seconds; defaults to `60`. |
| `ROOM_CREATE_RATE_LIMIT` | No | Protected-room creation requests allowed per IP per window; defaults to `10`. |
| `ROOM_CREATE_RATE_WINDOW_SECONDS` | No | Protected-room creation rate-limit window in seconds; defaults to `60`. |

`GET /api/ice-servers` returns the public STUN server and any configured TURN server credentials with `Cache-Control: no-store`. It accepts either `TURN_URLS` or the single-URL `TURN_URL` alias. Credentials are necessarily visible to clients using them; use the shared-secret mode for expiring credentials rather than embedding a permanent TURN password in the app.

`POST /api/conference-token` accepts `{ "room": "...", "username": "...", "userId": "..." }` and returns a short-lived signed token and LiveKit URL. `userId` is optional; when omitted, the server creates an identity. API endpoints allow cross-origin requests for native clients. The API secret is never sent to clients. Configure all three `LIVEKIT_*` variables to enable conference rooms. Invite links retain the selected call mode.

### Optional protected rooms

Protected rooms are disabled unless `ROOM_ACCESS_SECRET` is set. Leaving the variable unset preserves the existing open-room behavior. Protected IDs and access codes are derived statelessly with HMAC-SHA256, so deployments remain secure across Render restarts and sleeps. Never log or share `ROOM_ACCESS_SECRET`.

Create a protected room:

```http
POST /api/rooms
Content-Type: application/json

{}
```

Response:

```json
{"room":"pr1_<nonce>_<room-proof>","accessCode":"<access-code>"}
```

The protected room ID is still a valid `/room/{id}` URL. Put the access code in the invite URL query string, for example `/room/pr1_<nonce>_<room-proof>?accessCode=<access-code>`. Existing web and Android clients can open it as a normal room URL; clients that cannot send the code receive `403`.

The conference-token endpoint accepts the optional JSON or query field `accessCode`:

```http
POST /api/conference-token
Content-Type: application/json

{"room":"pr1_<nonce>_<room-proof>","username":"Alice","accessCode":"<access-code>"}
```

```text
GET /api/conference-token?room=...&username=Alice&accessCode=...
```

WebSocket joins accept the same optional query parameter:

```text
/ws?room=...&name=Alice&accessCode=...
```

Statuses:

| Endpoint | Status | Meaning |
| --- | --- | --- |
| `POST /api/rooms` | `200` | Protected room and access code created; response shape is `{"room":"...","accessCode":"..."}`. |
| `POST /api/rooms` | `400` | Invalid JSON or request. |
| `POST /api/rooms` | `503` | `ROOM_ACCESS_SECRET` is unset. |
| `/api/conference-token` | `403` | Protected room code is missing or wrong; body is plain text `access denied`. |
| `/api/conference-token` | `429` | Per-IP abuse limit reached; `Retry-After` is included. |
| `/ws` | `403` | Protected room code is missing or wrong. |
| `/api/rooms` and `/api/conference-token` | `429` | Per-IP abuse limit reached; `Retry-After` is included. |

The server compares submitted codes in constant time. Unknown JSON fields remain accepted for compatibility with newer and older clients.

## Render deployment

1. Push the repository to GitHub and create a Render Web Service using the Docker runtime (or let Render apply [render.yaml](./render.yaml)).
2. Set `TURN_URLS` and `TURN_SHARED_SECRET` for reliable peer-to-peer media on restrictive mobile/NAT networks. For conference rooms, also set `LIVEKIT_URL`, `LIVEKIT_API_KEY`, and `LIVEKIT_API_SECRET` in the Render dashboard.
3. Room protection and abuse limiting are optional. To enable room protection, set `ROOM_ACCESS_SECRET` to a high-entropy Render secret and keep it stable across restarts. The default limits are generous (`60` conference-token requests and `10` room creations per IP per minute). Commented optional examples are available in [render.yaml](./render.yaml).
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
