# webrtcV1

This is an upgraded version of the original WebRTC video chat service that supports **multi‑party mesh video calls** (2–10 participants) using only a simple WebSocket signaling server.

## Features

- Dynamic peer discovery & signaling
- Mesh topolgy: every participant connects to every other participant
- Automatic video element creation for each remote peer
- Join/leave notifications
- Same media controls as before (camera/mic toggle, hang up)

## Running

```bash
cd webrtcV1
go mod download
go run .
```

Open multiple browser windows/tabs at `http://localhost:8080` and start a call in each.

Set `PORT` when running behind a platform proxy:

```bash
PORT=10000 go run .
```

## Deploying to Render

1. Push this repository to GitHub.
2. Create a new Render **Web Service**.
3. Choose **Docker** as the runtime.
4. Render will detect [render.yaml](./render.yaml) and [Dockerfile](./Dockerfile).
5. Deploy; Render will terminate SSL and forward HTTPS traffic to the app over HTTP.

The application listens on the `PORT` environment variable and exposes `/healthz` for health checks.

## How it works

- Each client receives a unique ID and a list of existing peers when it connects
- Server broadcasts `new-peer`/`remove-peer` events and forwards SDP/candidate messages
- Clients maintain one `RTCPeerConnection` per remote peer and manage them in a map
- Remote video tracks are rendered in new `<video>` elements added to the grid

## Notes

This simple mesh approach works well for small groups (2‑10). Beyond that you’ll want a SFU/MCU.

## Project structure

```
webrtcV1/
├── main.go
├── signaling.go
├── go.mod
├── README.md
└── public/
    ├── index.html
    ├── css/style.css
    └── js/main.js
```
