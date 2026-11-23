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
| `DATABASE_URL` | Protected rooms | Neon PostgreSQL pooled connection string used by the app. |
| `DATABASE_URL_UNPOOLED` | Schema migration | Neon direct connection string, used only when running migrations. |
| `ROOM_ACCESS_SECRET` | No | Enables opt-in protected rooms. Keep this secret stable across deploys/restarts. |
| `CONFERENCE_TOKEN_RATE_LIMIT` | No | Conference-token requests allowed per IP per window; defaults to `60`. |
| `CONFERENCE_TOKEN_RATE_WINDOW_SECONDS` | No | Conference-token rate-limit window in seconds; defaults to `60`. |
| `ROOM_CREATE_RATE_LIMIT` | No | Protected-room creation requests allowed per IP per window; defaults to `10`. |
| `ROOM_CREATE_RATE_WINDOW_SECONDS` | No | Protected-room creation rate-limit window in seconds; defaults to `60`. |
| `ROOM_ACCESS_ATTEMPT_LIMIT` | No | Protected-room access attempts allowed per IP; defaults to `10`. |
| `ROOM_ACCESS_ATTEMPT_WINDOW_SECONDS` | No | Access-attempt window in seconds; defaults to `900`. |

`GET /api/ice-servers` returns the public STUN server and any configured TURN server credentials with `Cache-Control: no-store`. It accepts either `TURN_URLS` or the single-URL `TURN_URL` alias. Credentials are necessarily visible to clients using them; use the shared-secret mode for expiring credentials rather than embedding a permanent TURN password in the app.

`POST /api/conference-token` accepts `{ "room": "...", "username": "...", "userId": "..." }` and returns a short-lived signed token and LiveKit URL. `userId` is optional; when omitted, the server creates an identity. API endpoints allow cross-origin requests for native clients. The API secret is never sent to clients. Configure all three `LIVEKIT_*` variables to enable conference rooms. Invite links retain the selected call mode.

### Optional protected rooms and numeric meeting credentials

Protected-room creation and numeric ID/password entry require both `ROOM_ACCESS_SECRET` and a configured Neon `DATABASE_URL`. Keep the access secret stable across deployments; it is used to verify protected room IDs and derive password verifiers. Existing normal rooms and previously issued stateless invite links continue to work. Newly created protected rooms are stored in Neon so their 10-digit meeting IDs remain unique and survive restarts. Password verifiers, not the plaintext passwords, are stored in the database.

If `ROOM_ACCESS_SECRET` is removed or changed, new database-backed protected rooms fail closed and cannot be joined; they do not become open rooms. Existing normal rooms remain unaffected.

Run the versioned schema migration using the direct (unpooled) Neon URL:

```powershell
$env:DATABASE_URL_UNPOOLED = "<Neon direct connection string>"
go run ./cmd/migrate
```

Keep both database URLs and `ROOM_ACCESS_SECRET` out of source control. The repository ignores `.env` and `.env.local`.

Create a protected room from the web lobby by checking **Protect this room with an access code**, then choosing **New 1-on-1 call** or **New conference**. The lobby displays a 10-digit meeting ID and 6-digit password. Share either those credentials or the invite link.

Create a protected room through the API:

```http
POST /api/rooms
Content-Type: application/json

{}
```

Response:

```json
{"room":"pr2_<nonce>_<room-proof>","accessCode":"234567","meetingId":"7069567577"}
```

The protected room ID is still a valid `/room/{id}` URL. The web app places the password in the URL fragment (`/room/{id}#accessCode=234567`) so it is not sent as part of the page request; it also accepts the earlier `?accessCode=...` link format. Clients that cannot pass the password will be denied joining.

Numeric credential lookup:

```http
POST /api/rooms/resolve
Content-Type: application/json

{"meetingId":"7069567577","password":"234567"}
```

On success, the response is `{"room":"pr2_...","accessCode":"234567"}`. A missing/incorrect ID or password returns the same plain-text `403 access denied`. The web lobby has separate **Meeting ID** and **Password** fields and supports both 1-on-1 and conference joins. Existing custom room-code inputs remain a separate flow.

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
| `POST /api/rooms` | `200` | Protected room created; response shape is `{"room":"...","accessCode":"...","meetingId":"..."}`. |
| `POST /api/rooms` | `400` | Invalid JSON or request. |
| `POST /api/rooms` | `503` | `ROOM_ACCESS_SECRET`, database configuration, or the database is unavailable. |
| `POST /api/rooms/resolve` | `200` | Meeting ID/password verified and mapped to the protected room. |
| `POST /api/rooms/resolve` | `403` | Meeting ID/password pair is invalid. |
| `POST /api/rooms/resolve` | `429` | Access-attempt limit reached; `Retry-After` is included. |
| `/api/conference-token` | `403` | Protected room code is missing or wrong; body is plain text `access denied`. |
| `/api/conference-token` | `429` | Per-IP abuse limit reached; `Retry-After` is included. |
| `/ws` | `403` | Protected room code is missing or wrong. |
| `/api/rooms` and `/api/conference-token` | `429` | Per-IP abuse limit reached; `Retry-After` is included. |

The server compares submitted codes in constant time. Unknown JSON fields remain accepted for compatibility with newer and older clients.

## Render deployment

1. Push the repository to GitHub and create a Render Web Service using the Docker runtime (or let Render apply [render.yaml](./render.yaml)).
2. Set `TURN_URLS` and `TURN_SHARED_SECRET` for reliable peer-to-peer media on restrictive mobile/NAT networks. For conference rooms, also set `LIVEKIT_URL`, `LIVEKIT_API_KEY`, and `LIVEKIT_API_SECRET` in the Render dashboard.
3. Room protection is optional. Configure `DATABASE_URL` with the Neon pooled URL and set `ROOM_ACCESS_SECRET` to a high-entropy secret in Render. Keep the secret stable. Run `go run ./cmd/migrate` with `DATABASE_URL_UNPOOLED` before deploying the feature. The default limits are `60` conference-token requests and `10` room creations per IP per minute, plus `10` protected-room access attempts per IP per 15 minutes. Commented examples are available in [render.yaml](./render.yaml).
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
