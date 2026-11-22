export class RoomSignaling extends EventTarget {
  constructor(roomId, displayName) {
    super();
    this.roomId = roomId;
    this.displayName = displayName;
    this.socket = null;
    this.retryTimer = null;
    this.retryCount = 0;
    this.hasConnected = false;
    this.closedByUser = false;
  }

  connect() {
    this.closedByUser = false;
    this.openSocket();
  }

  openSocket() {
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const query = new URLSearchParams({ room: this.roomId, name: this.displayName });
    const socket = new WebSocket(`${protocol}//${window.location.host}/ws?${query}`);
    this.socket = socket;
    this.emit("state", { state: this.hasConnected ? "reconnecting" : "connecting" });

    socket.addEventListener("open", () => {
      const reconnecting = this.hasConnected;
      this.hasConnected = true;
      this.retryCount = 0;
      this.emit("state", { state: "connected", reconnecting });
    });

    socket.addEventListener("message", (event) => {
      try {
        const message = JSON.parse(event.data);
        if (message.type === "error" && message.data?.fatal) this.closedByUser = true;
        this.emit(message.type, message);
      } catch (error) {
        console.error("Invalid signaling message", error);
        this.emit("error", { message: "Received an invalid signaling message" });
      }
    });

    socket.addEventListener("error", () => {
      this.emit("state", { state: "connection error" });
    });

    socket.addEventListener("close", () => {
      if (this.closedByUser) return;
      this.emit("state", { state: "reconnecting" });
      this.scheduleReconnect();
    });
  }

  send(message) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return false;
    this.socket.send(JSON.stringify(message));
    return true;
  }

  close() {
    this.closedByUser = true;
    window.clearTimeout(this.retryTimer);
    this.socket?.close(1000, "Leaving room");
  }

  emit(type, detail) {
    this.dispatchEvent(new CustomEvent(type, { detail }));
  }

  scheduleReconnect() {
    window.clearTimeout(this.retryTimer);
    const delay = Math.min(1000 * (2 ** this.retryCount), 30000);
    this.retryCount += 1;
    this.retryTimer = window.setTimeout(() => {
      if (!this.closedByUser) this.openSocket();
    }, delay);
  }
}
