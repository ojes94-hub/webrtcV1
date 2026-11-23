import { CallSession } from "./call.js";
import { ConferenceSession } from "./conference.js";

const $ = (id) => document.getElementById(id);
const lobby = $("lobby");
const callView = $("call-view");
const roomCodeInput = $("roomCode");
const displayNameInput = $("displayName");
const previewVideo = $("previewVideo");
const previewPlaceholder = $("preview-placeholder");
const previewState = $("preview-state");
const previewName = $("preview-name");
const lobbyError = $("lobby-error");
const cameraSelect = $("cameraSelect");
const microphoneSelect = $("microphoneSelect");
const speakerSelect = $("speakerSelect");
const videoGrid = $("video-grid");
const deviceSettings = document.querySelector(".device-settings");
const remoteTiles = new Map();
const participantById = new Map();
const connectionStates = new Map();
const screenShareSuffix = "\u0000screen-share";

function screenShareTileKey(peerId) {
  return `${peerId}${screenShareSuffix}`;
}

function isScreenShareTileKey(tileKey) {
  return tileKey.endsWith(screenShareSuffix);
}

function participantIdForTileKey(tileKey) {
  return isScreenShareTileKey(tileKey)
    ? tileKey.slice(0, -screenShareSuffix.length)
    : tileKey;
}

let roomId = "";
let roomAccessCode = "";
let previewStream = null;
let callSession = null;
let callMode = "p2p";
let unreadMessages = 0;
let activePanel = "";
let audioEnabled = true;
let videoEnabled = true;
let speakerDeviceId = "";
let raisedHand = false;
let captionsEnabled = false;
let mobilePinnedPeerId = "";
let joinNoticeTimer = null;

function showJoinToast(message) {
  const toast = $("app-toast");
  toast.textContent = message;
  toast.hidden = false;
  window.clearTimeout(joinNoticeTimer);
  joinNoticeTimer = window.setTimeout(() => { toast.hidden = true; }, 5000);
}

function isValidRoomCode(value) {
  return /^[A-Za-z0-9_-]{3,64}$/.test(value);
}

function roomFromLocation() {
  const pathMatch = window.location.pathname.match(/^\/room\/([^/]+)\/?$/);
  const value = pathMatch?.[1] || new URLSearchParams(window.location.search).get("room") || "";
  return isValidRoomCode(value) ? value : "";
}

function setRoom(room, mode = callMode) {
  roomId = room;
  callMode = mode;
  updateJoinModeButtons();
  roomCodeInput.value = room;
  $("room-label").textContent = `Room ${room}`;
  $("call-room-name").textContent = `Room ${room}`;
  $("room-toolbar").hidden = !room;
  $("lobbyCopyLink").hidden = !room;
  const params = new URLSearchParams();
  if (mode === "conference") params.set("mode", "conference");
  if (roomAccessCode) params.set("accessCode", roomAccessCode);
  const suffix = params.size ? `?${params.toString()}` : "";
  window.history.replaceState({}, "", room ? `/room/${encodeURIComponent(room)}${suffix}` : "/");
}

function updateJoinModeButtons() {
  const isConference = callMode === "conference";
  $("joinP2P").textContent = isConference ? "Join conference" : "Join 1-on-1";
  $("joinConference").textContent = isConference ? "Join 1-on-1 instead" : "Join conference";
}

function parseRoomInput(value) {
  const input = value.trim();
  if (!input) return "";
  try {
    const url = new URL(input, window.location.origin);
    const match = url.pathname.match(/^\/room\/([^/]+)\/?$/);
    if (match) return decodeURIComponent(match[1]);
  } catch {
    return "";
  }
  return input;
}

function createRoomCode() {
  if (crypto.randomUUID) return crypto.randomUUID().replaceAll("-", "");
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join("");
}

function saveName() {
  const name = displayNameInput.value.trim();
  previewName.textContent = name || "Your name";
  try {
    localStorage.setItem("meet-display-name", name);
  } catch (error) {
    console.warn("Could not save display name", error);
  }
  return name;
}

function mediaConstraints() {
  const video = cameraSelect.value
    ? { deviceId: { exact: cameraSelect.value }, width: { ideal: 1280 }, height: { ideal: 720 } }
    : { facingMode: { ideal: "user" }, width: { ideal: 1280 }, height: { ideal: 720 } };
  const audio = {
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true
  };
  if (microphoneSelect.value) audio.deviceId = { exact: microphoneSelect.value };
  return { audio, video };
}

async function getMedia() {
  if (!navigator.mediaDevices?.getUserMedia) {
    return null;
  }
  try {
    return await navigator.mediaDevices.getUserMedia(mediaConstraints());
  } catch (videoError) {
    console.warn("Combined camera/microphone access failed; retrying audio-only", videoError);
    try {
      return await navigator.mediaDevices.getUserMedia({ video: false, audio: mediaConstraints().audio });
    } catch (audioError) {
      console.warn("Audio-only access failed; joining receive-only", audioError);
      return null;
    }
  }
}

async function refreshDevices() {
  if (!navigator.mediaDevices?.enumerateDevices) return;
  const selected = {
    camera: cameraSelect.value,
    microphone: microphoneSelect.value,
    speaker: speakerSelect.value
  };
  const devices = await navigator.mediaDevices.enumerateDevices();
  fillDeviceSelect(cameraSelect, devices.filter((device) => device.kind === "videoinput"), "Default camera", selected.camera);
  fillDeviceSelect(microphoneSelect, devices.filter((device) => device.kind === "audioinput"), "Default microphone", selected.microphone);
  fillDeviceSelect(speakerSelect, devices.filter((device) => device.kind === "audiooutput"), "Default speaker", selected.speaker);
  speakerSelect.disabled = typeof HTMLMediaElement.prototype.setSinkId !== "function";
}

function fillDeviceSelect(select, devices, defaultLabel, selectedId) {
  select.replaceChildren(new Option(defaultLabel, ""));
  devices.forEach((device, index) => {
    select.add(new Option(device.label || `${defaultLabel.replace("Default ", "")} ${index + 1}`, device.deviceId));
  });
  if (devices.some((device) => device.deviceId === selectedId)) select.value = selectedId;
}

async function togglePreview() {
  lobbyError.textContent = "";
  if (previewStream) {
    previewStream.getTracks().forEach((track) => track.stop());
    previewStream = null;
    previewVideo.srcObject = null;
    previewPlaceholder.hidden = false;
    previewState.textContent = "Camera off";
    $("startPreview").textContent = "Enable camera preview";
    $("startPreview").setAttribute("aria-pressed", "false");
    return;
  }
  try {
    previewStream = await getMedia();
    if (!previewStream) {
      previewPlaceholder.hidden = false;
      previewState.textContent = "No camera or mic detected";
      $("startPreview").textContent = "Preview unavailable";
      $("startPreview").setAttribute("aria-pressed", "false");
      lobbyError.textContent = "You can still join in listen/view-only mode.";
      return;
    }
    previewVideo.srcObject = previewStream;
    previewPlaceholder.hidden = true;
    previewState.textContent = "Camera and mic ready";
    $("startPreview").textContent = "Turn preview off";
    $("startPreview").setAttribute("aria-pressed", "true");
    await refreshDevices();
  } catch (error) {
    lobbyError.textContent = `Could not start preview: ${error.message}`;
  }
}

async function joinRoom(room, mode = callMode) {
  const name = saveName();
  if (!name) {
    lobbyError.textContent = "Enter your name before joining.";
    displayNameInput.focus();
    return;
  }
  if (!isValidRoomCode(room)) {
    lobbyError.textContent = "Enter a valid room code (3–64 letters, numbers, dashes, or underscores).";
    return;
  }

  lobbyError.textContent = "";
  roomAccessCode = $("accessCode").value.trim();
  setRoom(room, mode);
  $("chat-messages").replaceChildren();
  $("unread-badge").hidden = true;
  unreadMessages = 0;
  connectionStates.clear();
  remoteTiles.clear();
  participantById.clear();
  document.querySelectorAll(".remote-tile").forEach((tile) => tile.remove());
  document.querySelectorAll(".main-stage > .video-tile").forEach((tile) => {
    tile.classList.remove("pinned");
    videoGrid.append(tile);
  });
  videoGrid.classList.remove("has-pinned");
  $("empty-call").hidden = false;
  try {
    localStorage.setItem("meet-display-name", name);
  } catch (error) {
    console.warn("Could not save display name", error);
  }
  let stream = previewStream;
  if (!stream || stream.getTracks().some((track) => track.readyState !== "live")) {
    stream = await getMedia();
  }
  if (!stream) {
    showJoinToast("Joined in listen/view-only mode — no camera or mic active");
  }
  previewStream = null;
  previewVideo.srcObject = null;
  lobby.hidden = true;
  callView.hidden = false;
  document.body.classList.add("in-call");
  $("connection-indicator").hidden = false;
  $("localName").textContent = name;
  $("localMicState").textContent = "Mic off";
  audioEnabled = Boolean(stream?.getAudioTracks().some((track) => track.enabled));
  videoEnabled = Boolean(stream?.getVideoTracks().some((track) => track.enabled));
  updateControlState();
  const Session = mode === "conference" ? ConferenceSession : CallSession;
  callSession = new Session({
    roomId: room,
    displayName: name,
    accessCode: roomAccessCode,
    localStream: stream,
    localVideo: $("localVideo"),
    onStatus: updateCallStatus,
    onSignalingState: updateSignalingState,
    onConnectionState: updatePeerConnectionState,
    onParticipants: updateParticipants,
    onRemoteStream: updateRemoteStream,
    onChat: appendChatMessage,
    onSpeaker: updateSpeaker
  });
  try {
    await callSession.start();
    await refreshDevices();
  } catch (error) {
    lobby.hidden = false;
    callView.hidden = true;
    document.body.classList.remove("in-call");
    $("connection-indicator").hidden = true;
    await callSession.leave();
    callSession = null;
    lobbyError.textContent = `Could not join the room: ${error.message}`;
  }
}

function updateSignalingState(state) {
  $("connection-label").textContent = state;
  $("connection-indicator").classList.toggle("connected", state === "connected");
  $("connection-indicator").classList.toggle("failed", state === "connection error");
  if (state === "reconnecting") $("call-connection-state").textContent = "Reconnecting to room…";
  if (state === "connected" && connectionStates.size === 0) $("call-connection-state").textContent = "Waiting for participants";
}

function updateCallStatus(message) {
  $("call-connection-state").textContent = message;
}

function updatePeerConnectionState({ peerId, connection, ice }) {
  connectionStates.set(peerId, { connection, ice });
  const states = [...connectionStates.values()];
  if (states.some((state) => state.connection === "connected")) {
    $("call-connection-state").textContent = "Media connected";
  } else if (states.some((state) => state.connection === "failed" || state.ice === "failed")) {
    $("call-connection-state").textContent = "Media route failed — TURN may be needed";
  } else {
    $("call-connection-state").textContent = states.length
      ? `Connecting media · ICE ${states[states.length - 1].ice}`
      : "Waiting for participants";
  }
}

function updateMobileGridLayout() {
  const count = remoteTiles.size;
  const mobile = window.matchMedia("(max-width: 767px)").matches;
  const stage = $("main-stage");
  const callMain = $("call-main");
  const remotes = [...remoteTiles.entries()];
  remotes.forEach(([, tile]) => {
    tile.classList.remove("mobile-overflow-hidden");
    if (mobile) tile.classList.remove("pinned");
    if (tile.parentElement === stage && (mobile || (count > 1 && !tile.classList.contains("pinned")))) {
      videoGrid.append(tile);
    }
  });
  videoGrid.classList.remove("remote-count-1", "remote-count-2", "remote-count-3", "remote-count-4");
  if (count > 0) videoGrid.classList.add(`remote-count-${Math.min(count, 4)}`);
  $("empty-call").hidden = count > 0;
  callMain.classList.toggle("mobile-group", mobile && count > 1);
  callMain.classList.toggle("mobile-solo", mobile && count === 1);
  $("mobile-overflow-pill").hidden = !mobile || count <= 5;
  $("mobile-overflow-pill").textContent = count > 5 ? `+${count - 5} more` : "";
  if (mobile && count > 0) {
    const selected = remotes.find(([tileKey]) => tileKey === mobilePinnedPeerId) ||
      remotes.find(([tileKey]) => isScreenShareTileKey(tileKey)) ||
      remotes.find(([tileKey]) => participantById.get(participantIdForTileKey(tileKey))?.presenting) ||
      remotes.find(([tileKey]) => participantById.get(participantIdForTileKey(tileKey))?.speaking) ||
      remotes[0];
    stage.append(selected[1]);
    $("active-empty").hidden = true;
    remotes.filter(([, tile]) => tile !== selected[1]).forEach(([, tile]) => {
      tile.classList.remove("mobile-overflow-hidden");
    });
  } else if (!mobile && count === 1) {
    stage.append(remotes[0][1]);
    remotes[0][1].classList.add("pinned");
    $("active-empty").hidden = true;
  } else {
    $("active-empty").hidden = Boolean(stage.querySelector(".video-tile"));
  }
}

function updateRemoteStream(peerId, stream, source = "camera") {
  const isScreenShare = source === "screen-share";
  const tileKey = isScreenShare ? screenShareTileKey(peerId) : peerId;
  let tile = remoteTiles.get(tileKey);
  if (!stream) {
    tile?.remove();
    remoteTiles.delete(tileKey);
    if (!isScreenShare) connectionStates.delete(peerId);
    $("empty-call").hidden = remoteTiles.size > 0;
    updateMobileGridLayout();
    return;
  }
  if (!tile) {
    tile = document.createElement("article");
    tile.className = `video-tile remote-tile${isScreenShare ? " screen-share-tile" : ""}`;
    tile.dataset.peerId = peerId;
    tile.dataset.trackSource = source;
    const video = document.createElement("video");
    video.autoplay = true;
    video.playsInline = true;
    video.muted = true;
    video.setAttribute("aria-label", "Remote participant video");
    const audio = document.createElement("audio");
    audio.className = "remote-audio";
    audio.autoplay = true;
    audio.playsInline = true;
    const participant = participantById.get(peerId);
    video.addEventListener("click", () => {
      video.play().catch(() => {});
      audio.play().catch((error) => {
        console.info("Remote audio playback needs user interaction", error);
        $("call-connection-state").textContent = "Tap a participant video to enable audio";
      });
    });
    const shade = document.createElement("div");
    shade.className = "tile-shade";
    const avatar = document.createElement("div");
    avatar.className = "video-avatar";
    avatar.hidden = true;
    const avatarLetter = document.createElement("span");
    avatarLetter.textContent = (participant?.name || "G").slice(0, 1).toUpperCase();
    avatar.append(avatarLetter);
    const caption = document.createElement("div");
    caption.className = "tile-caption";
    const name = document.createElement("span");
    name.className = "participant-name";
    name.textContent = isScreenShare
      ? `${participant?.name || "Guest"} · Presenting`
      : participant?.name || "Guest";
    const mic = document.createElement("span");
    mic.className = "mic-state";
    mic.textContent = participant?.muted ? "Mic off" : "Mic on";
    mic.hidden = isScreenShare;
    caption.append(name, mic);
    const hand = document.createElement("span");
    hand.className = "hand-badge";
    hand.textContent = "✋";
    hand.hidden = true;
    tile.append(video, audio, avatar, shade, caption, hand);
    tile.addEventListener("click", () => togglePinnedTile(tile));
    tile.classList.toggle("speaking", Boolean(participant?.speaking));
    tile.classList.toggle("video-off", Boolean(participant?.videoOff));
    videoGrid.append(tile);
    remoteTiles.set(tileKey, tile);
    $("empty-call").hidden = true;
    if (speakerDeviceId && typeof audio.setSinkId === "function") {
      audio.setSinkId(speakerDeviceId).catch((error) => console.warn("Could not select speaker device", error));
    }
  }
  updateMobileGridLayout();
  const video = tile.querySelector("video");
  const audio = tile.querySelector(".remote-audio");
  const hasVideo = Boolean(stream.getVideoTracks().some((track) => track.readyState === "live"));
  tile.classList.toggle("video-off", !hasVideo);
  const avatar = tile.querySelector(".video-avatar");
  if (avatar) avatar.hidden = hasVideo;
  if (video.srcObject !== stream) {
    video.srcObject = stream;
    video.play().catch((error) => {
      console.info("Remote video playback needs user interaction", error);
      if (error.name === "NotAllowedError") {
        $("call-connection-state").textContent = "Tap a participant video to start playback";
      }
    });
  }
  if (audio.srcObject !== stream) {
    audio.srcObject = stream;
    audio.play().catch((error) => console.info("Remote audio playback needs user interaction", error));
  }
}

function updateParticipants(participants) {
  participantById.clear();
  participants.forEach((participant) => participantById.set(participant.id, participant));
  $("participant-count").textContent = `${participants.length} ${participants.length === 1 ? "person" : "people"}`;
  $("chat-participant-count").textContent = `${participants.length} ${participants.length === 1 ? "participant" : "participants"} in room`;
  $("participant-badge").textContent = String(participants.length);
  $("participant-badge").hidden = false;
  const list = $("participant-list");
  list.replaceChildren();
  const avatars = $("chat-participant-avatars");
  avatars.replaceChildren();
  participants.slice(0, 5).forEach((participant) => {
    const avatar = document.createElement("span");
    avatar.className = "chat-participant-avatar";
    avatar.textContent = (participant.name || "?").slice(0, 1).toUpperCase();
    avatar.title = participant.name;
    avatars.append(avatar);
  });
  if (participants.length > 5) {
    const overflow = document.createElement("span");
    overflow.className = "chat-participant-avatar";
    overflow.textContent = `+${participants.length - 5}`;
    avatars.append(overflow);
  }

  participants.forEach((participant) => {
    const name = participant.local ? `${participant.name} (you)` : participant.name;
    if (participant.local) {
      $("localName").textContent = name;
      $("localMicState").textContent = participant.muted ? "Mic off" : "Mic on";
      $("local-tile").classList.toggle("speaking", Boolean(participant.speaking));
      $("local-tile").classList.toggle("video-off", Boolean(participant.videoOff));
      $("local-avatar").hidden = !participant.videoOff;
      $("local-avatar-letter").textContent = (participant.name || "Y").slice(0, 1).toUpperCase();
      $("local-hand").hidden = !participant.raisedHand;
    } else {
      const tile = remoteTiles.get(participant.id);
      if (tile) {
        tile.querySelector(".participant-name").textContent = participant.name;
        tile.querySelector(".mic-state").textContent = participant.muted ? "Mic off" : "Mic on";
        tile.classList.toggle("speaking", Boolean(participant.speaking));
        tile.classList.toggle("video-off", Boolean(participant.videoOff));
        const avatar = tile.querySelector(".video-avatar");
        if (avatar) {
          avatar.hidden = !participant.videoOff;
          avatar.querySelector("span").textContent = (participant.name || "G").slice(0, 1).toUpperCase();
        }
        tile.querySelector(".hand-badge").hidden = !participant.raisedHand;
      }
      const screenTile = remoteTiles.get(screenShareTileKey(participant.id));
      if (screenTile) {
        screenTile.querySelector(".participant-name").textContent = `${participant.name} · Presenting`;
      }
    }
    const item = document.createElement("li");
    item.className = "participant-item";
    const avatar = document.createElement("span");
    avatar.className = "participant-avatar";
    avatar.textContent = (participant.name || "?").slice(0, 1).toUpperCase();
    const label = document.createElement("span");
    label.textContent = name;
    const meta = document.createElement("span");
    meta.className = "participant-meta";
    meta.textContent = participant.muted ? "Muted" : participant.speaking ? "Speaking" : "In room";
    item.append(avatar, label, meta);
    list.append(item);
  });
  const presenter = participants.find((participant) => participant.presenting);
  $("presenter-banner").hidden = !presenter;
  if (presenter) {
    $("presenter-name").textContent = `${presenter.name} is presenting`;
    $("presenter-avatar").textContent = (presenter.name || "?").slice(0, 1).toUpperCase();
    if (window.matchMedia("(min-width: 768px)").matches) {
      const presenterTile = presenter.local
        ? $("local-tile")
        : remoteTiles.get(screenShareTileKey(presenter.id)) || remoteTiles.get(presenter.id);
      if (presenterTile && presenterTile.parentElement !== $("main-stage")) {
        document.querySelectorAll(".main-stage > .video-tile").forEach((tile) => {
          tile.classList.remove("pinned");
          videoGrid.append(tile);
        });
        presenterTile.classList.add("pinned");
        $("main-stage").append(presenterTile);
        $("active-empty").hidden = true;
      }
    }
  }
  updateMobileGridLayout();
}

function updateSpeaker(peerId, speaking) {
  const tile = peerId === "local" ? $("local-tile") : remoteTiles.get(peerId);
  tile?.classList.toggle("speaking", speaking);
  if (peerId !== "local" && speaking) updateMobileGridLayout();
}

function appendChatMessage(message) {
  const line = document.createElement("div");
  line.className = `chat-line${message.mine ? " mine" : ""}`;
  if (!message.mine) {
    const avatar = document.createElement("span");
    avatar.className = "chat-avatar";
    avatar.textContent = (message.name || "?").slice(0, 1).toUpperCase();
    line.append(avatar);
  }
  const item = document.createElement("div");
  item.className = `chat-message${message.mine ? " mine" : ""}`;
  const author = document.createElement("strong");
  author.textContent = message.name;
  const text = document.createElement("span");
  text.textContent = message.text;
  item.append(author, text);
  line.append(item);
  $("chat-messages").append(line);
  $("chat-messages").scrollTop = $("chat-messages").scrollHeight;
  if (!message.mine && activePanel !== "chat") {
    unreadMessages += 1;
    $("unread-badge").textContent = String(unreadMessages);
    $("unread-badge").hidden = false;
  }
}

function openPanel(panel) {
  const sidePanel = $("side-panel");
  if (activePanel === panel && !sidePanel.hidden) {
    closePanel();
    return;
  }
  activePanel = panel;
  sidePanel.hidden = false;
  $("panel-backdrop").hidden = !window.matchMedia("(max-width: 767px)").matches;
  $("participants-panel").hidden = panel !== "people" && panel !== "info";
  $("chat-panel").hidden = panel !== "chat";
  $("panel-title").textContent =
    panel === "people" ? "Participants" :
    panel === "info" ? "Meeting info" : "Meeting chat";
  if (panel === "chat") {
    unreadMessages = 0;
    $("unread-badge").hidden = true;
    $("chat-input").focus();
  }
}

function closePanel() {
  $("side-panel").hidden = true;
  $("panel-backdrop").hidden = true;
  activePanel = "";
}

window.addEventListener("resize", updateMobileGridLayout);

function togglePinnedTile(tile) {
  if (window.matchMedia("(max-width: 767px)").matches) {
    if (tile.id === "local-tile") withCall(() => callSession.switchCamera());
    else {
      const peerId = tile.dataset.peerId;
      const tileKey = tile.dataset.trackSource === "screen-share" ? screenShareTileKey(peerId) : peerId;
      mobilePinnedPeerId = mobilePinnedPeerId === tileKey ? "" : tileKey;
      updateMobileGridLayout();
    }
    return;
  }
  const isPinned = tile.classList.contains("pinned");
  document.querySelectorAll(".main-stage > .video-tile").forEach((item) => {
    item.classList.remove("pinned");
    videoGrid.append(item);
  });
  $("active-empty").hidden = true;
  if (!isPinned) {
    tile.classList.add("pinned");
    $("main-stage").append(tile);
  } else {
    $("active-empty").hidden = false;
  }
}

function updateControlState() {
  $("audioToggle").setAttribute("aria-pressed", String(!audioEnabled));
  $("audioToggle").querySelector("span:last-of-type").textContent = audioEnabled ? "Mute" : "Unmute";
  $("mobileAudio").setAttribute("aria-pressed", String(!audioEnabled));
  $("localMicState").textContent = audioEnabled ? "Mic on" : "Mic off";
  $("videoToggle").setAttribute("aria-pressed", String(!videoEnabled));
  $("videoToggle").querySelector("span:last-of-type").textContent = videoEnabled ? "Camera" : "Camera off";
  $("mobileVideo").setAttribute("aria-pressed", String(!videoEnabled));
  $("local-tile").classList.toggle("video-off", !videoEnabled);
}

async function withCall(operation) {
  if (!callSession) return;
  try {
    await operation();
  } catch (error) {
    $("call-connection-state").textContent = error.message;
    console.error(error);
  }
}

async function leaveCall() {
  if (callSession) await callSession.leave();
  callSession = null;
  remoteTiles.clear();
  mobilePinnedPeerId = "";
  participantById.clear();
  connectionStates.clear();
  document.querySelectorAll(".remote-tile").forEach((tile) => tile.remove());
  videoGrid.classList.remove("has-pinned");
  updateMobileGridLayout();
  $("empty-call").hidden = false;
  closePanel();
  callView.hidden = true;
  lobby.hidden = false;
  document.body.classList.remove("in-call");
  $("connection-indicator").hidden = true;
  $("screenShare").classList.remove("active");
  $("local-tile").classList.remove("sharing", "pinned", "video-off", "speaking");
  $("participant-list").replaceChildren();
  $("participant-badge").hidden = true;
  $("presenter-banner").hidden = true;
  raisedHand = false;
  $("raiseHand").setAttribute("aria-pressed", "false");
  previewState.textContent = "Camera off";
  previewPlaceholder.hidden = false;
  $("startPreview").textContent = "Enable camera preview";
}

async function copyRoomLink() {
  if (!roomId) return;
  try {
    const params = new URLSearchParams();
    if (callMode === "conference") params.set("mode", "conference");
    if (roomAccessCode) params.set("accessCode", roomAccessCode);
    const suffix = params.size ? `?${params.toString()}` : "";
    await navigator.clipboard.writeText(`${window.location.origin}/room/${encodeURIComponent(roomId)}${suffix}`);
    $("copyRoomLink").textContent = "Copied!";
    $("lobbyCopyLink").textContent = "Invite link copied";
    $("panelCopyLink").textContent = "Invite link copied";
    window.setTimeout(() => {
      $("copyRoomLink").textContent = "Copy invite link";
      $("lobbyCopyLink").textContent = "Copy invite link";
      $("panelCopyLink").textContent = "Copy meeting link";
    }, 1600);
  } catch (error) {
    lobbyError.textContent = `Could not copy link: ${error.message}`;
  }
}

async function createProtectedRoom(mode) {
  if (!$("createProtected").checked) {
    setRoom(createRoomCode(), mode);
    roomAccessCode = "";
    setRoom(roomId, mode);
    lobbyError.textContent = mode === "conference"
      ? "Conference link ready. Share it, then join when you’re ready."
      : "Meeting link ready. Share it, then join when you’re ready.";
    return;
  }

  try {
    const response = await fetch("/api/rooms", {
      method: "POST",
      cache: "no-store",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    });
    if (!response.ok) {
      if (response.status === 503) {
        lobbyError.textContent = "Protected rooms are not enabled on this server. Set ROOM_ACCESS_SECRET on the server, then try again.";
        return;
      }
      lobbyError.textContent = `Could not create protected room (${response.status}).`;
      return;
    }
    const created = await response.json();
    if (!created.room || !created.accessCode) {
      lobbyError.textContent = "Could not create protected room: the server returned an incomplete response.";
      return;
    }
    roomAccessCode = created.accessCode;
    $("accessCode").value = created.accessCode;
    setRoom(created.room, mode);
    lobbyError.textContent = "Protected meeting link ready. Share it, then join when you’re ready.";
  } catch (error) {
    lobbyError.textContent = `Could not create protected room: ${error.message}`;
  }
}

$("createMeeting").addEventListener("click", () => createProtectedRoom("p2p"));
$("createConference").addEventListener("click", () => createProtectedRoom("conference"));
$("lobbyCopyLink").addEventListener("click", async () => {
  if (!roomId) {
    lobbyError.textContent = "Create a meeting first.";
    return;
  }
  await copyRoomLink();
});
$("joinForm").addEventListener("submit", (event) => {
  event.preventDefault();
  const room = parseRoomInput(roomCodeInput.value);
  if (!isValidRoomCode(room)) {
    lobbyError.textContent = "Enter a valid room code or meeting link.";
    return;
  }
  joinRoom(room, callMode);
});
$("joinConference").addEventListener("click", () => {
  const room = parseRoomInput(roomCodeInput.value);
  if (!isValidRoomCode(room)) {
    lobbyError.textContent = "Enter a valid room code or meeting link.";
    return;
  }
  joinRoom(room, callMode === "conference" ? "p2p" : "conference");
});
$("startPreview").addEventListener("click", togglePreview);
$("previewMic").addEventListener("click", () => {
  if (!previewStream) return;
  const track = previewStream.getAudioTracks()[0];
  if (track) {
    track.enabled = !track.enabled;
    $("previewMic").setAttribute("aria-pressed", String(!track.enabled));
  }
});
$("copyRoomLink").addEventListener("click", copyRoomLink);
displayNameInput.addEventListener("input", saveName);

cameraSelect.addEventListener("change", () => {
  if (callSession) withCall(() => callSession.setCameraDevice(cameraSelect.value));
  else if (previewStream) togglePreview().then(togglePreview);
});
microphoneSelect.addEventListener("change", () => {
  if (callSession) withCall(() => callSession.setAudioInput(microphoneSelect.value));
  else if (previewStream) togglePreview().then(togglePreview);
});
speakerSelect.addEventListener("change", () => {
  speakerDeviceId = speakerSelect.value;
  if (callSession) {
    withCall(() => callSession.setAudioOutput(speakerSelect.value, document.querySelectorAll(".remote-audio")));
  }
});

$("audioToggle").addEventListener("click", () => withCall(async () => {
  audioEnabled = !audioEnabled;
  await callSession.setAudioEnabled(audioEnabled);
  updateControlState();
}));
$("videoToggle").addEventListener("click", () => withCall(async () => {
  videoEnabled = !videoEnabled;
  await callSession.setVideoEnabled(videoEnabled);
  updateControlState();
}));
$("screenShare").addEventListener("click", () => withCall(async () => {
  if (callSession.displayTrack) {
    await callSession.stopScreenShare();
    $("screenShare").classList.remove("active");
    $("local-tile").classList.remove("sharing");
  } else {
    await callSession.startScreenShare();
    $("screenShare").classList.add("active");
    $("local-tile").classList.add("sharing");
  }
}));
$("switchCamera").addEventListener("click", () => withCall(() => callSession.switchCamera()));
$("pipButton").addEventListener("click", async () => {
  const video = document.querySelector(".video-tile.pinned video") || $("localVideo");
  try {
    if (document.pictureInPictureElement) await document.exitPictureInPicture();
    else if (document.pictureInPictureEnabled) await video.requestPictureInPicture();
    else throw new Error("Picture-in-picture is not supported by this browser");
  } catch (error) {
    $("call-connection-state").textContent = error.message;
  }
});
$("participantsButton").addEventListener("click", () => openPanel("people"));
$("chatButton").addEventListener("click", () => openPanel("chat"));
$("closePanel").addEventListener("click", () => {
  closePanel();
});
$("panel-backdrop").addEventListener("click", closePanel);
$("hangupButton").addEventListener("click", leaveCall);
$("mobileHangup").addEventListener("click", leaveCall);
$("mobileChat").addEventListener("click", () => openPanel("chat"));
$("mobileParticipants").addEventListener("click", () => openPanel("people"));
$("mobileAudio").addEventListener("click", () => $("audioToggle").click());
$("mobileVideo").addEventListener("click", () => $("videoToggle").click());
$("mobileSwitchCamera").addEventListener("click", () => withCall(() => callSession.switchCamera()));
$("mobileAudioOutput").addEventListener("click", () => {
  if (!speakerSelect.value && speakerSelect.options.length > 1) speakerSelect.selectedIndex = 1;
  speakerSelect.dispatchEvent(new Event("change"));
});
$("infoButton").addEventListener("click", () => openPanel("info"));
$("panelCopyLink").addEventListener("click", copyRoomLink);
$("captionsToggle").addEventListener("click", () => {
  captionsEnabled = !captionsEnabled;
  $("captionsToggle").setAttribute("aria-pressed", String(captionsEnabled));
  $("call-connection-state").textContent = captionsEnabled ? "Captions enabled" : "Captions disabled";
});
$("raiseHand").addEventListener("click", () => {
  raisedHand = !raisedHand;
  $("raiseHand").setAttribute("aria-pressed", String(raisedHand));
  withCall(() => callSession.toggleRaiseHand(raisedHand));
});
$("moreButton").addEventListener("click", () => {
  $("more-menu").hidden = !$("more-menu").hidden;
});
$("deviceSettings").addEventListener("click", () => {
  $("more-menu").hidden = true;
  openPanel("info");
});
$("chat-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const input = $("chat-input");
  const text = input.value.trim();
  if (!text || !callSession) return;
  withCall(async () => {
    if (!await callSession.handleLocalChat(text)) {
      $("call-connection-state").textContent = "Chat not sent — reconnecting";
      return;
    }
    input.value = "";
  });
});

displayNameInput.value = (() => {
  try {
    return localStorage.getItem("meet-display-name") || "";
  } catch {
    return "";
  }
})();
displayNameInput.dispatchEvent(new Event("input"));
if (window.matchMedia("(max-width: 767px)").matches) deviceSettings.open = false;
roomId = roomFromLocation();
if (roomId) {
  const locationParams = new URLSearchParams(window.location.search);
  roomAccessCode = (locationParams.get("accessCode") || "").trim();
  $("accessCode").value = roomAccessCode;
  callMode = locationParams.get("mode") === "conference" ? "conference" : "p2p";
  updateJoinModeButtons();
  roomCodeInput.value = roomId;
  $("room-label").textContent = `Room ${roomId}`;
  $("room-toolbar").hidden = false;
  $("lobbyCopyLink").hidden = false;
}
refreshDevices().catch((error) => console.warn("Could not enumerate media devices", error));

window.addEventListener("pagehide", () => {
  previewStream?.getTracks().forEach((track) => track.stop());
  callSession?.leave();
});
