import { RoomSignaling } from "./signaling.js";

export class CallSession {
  constructor(options) {
    this.roomId = options.roomId;
    this.displayName = options.displayName;
    this.localVideo = options.localVideo;
    this.onStatus = options.onStatus;
    this.onSignalingState = options.onSignalingState;
    this.onConnectionState = options.onConnectionState;
    this.onParticipants = options.onParticipants;
    this.onRemoteStream = options.onRemoteStream;
    this.onChat = options.onChat;
    this.onSpeaker = options.onSpeaker;
    this.localStream = options.localStream;
    this.signaling = new RoomSignaling(this.roomId, this.displayName);
    this.iceServers = null;
    this.clientId = "";
    this.participants = new Map();
    this.peerConnections = new Map();
    this.videoSenders = new Map();
    this.audioSenders = new Map();
    this.pendingCandidates = new Map();
    this.speakerDetectors = new Map();
    this.restartingPeers = new Set();
    this.restartTimers = new Map();
    this.audioContext = null;
    this.cameraTrack = this.localStream.getVideoTracks()[0] || null;
    this.displayTrack = null;
    this.facingMode = "user";
    this.raisedHand = false;
    this.presenting = false;
    this.audioEnabled = this.localStream.getAudioTracks().some((track) => track.enabled);
    this.videoEnabled = Boolean(this.cameraTrack?.enabled);
    this.isStarted = false;
  }

  async start() {
    this.iceServers = await this.loadIceServers();
    this.localVideo.srcObject = this.localStream;
    this.bindSignaling();
    this.signaling.connect();
    this.isStarted = true;
    this.watchSpeaker("local", this.localStream);
  }

  async loadIceServers() {
    const response = await fetch("/api/ice-servers", { cache: "no-store" });
    if (!response.ok) throw new Error(`ICE server configuration failed (${response.status})`);
    const config = await response.json();
    if (!Array.isArray(config.iceServers) || config.iceServers.length === 0) {
      throw new Error("No ICE servers are configured");
    }
    return config.iceServers;
  }

  bindSignaling() {
    this.signaling.addEventListener("state", (event) => {
      const { state, reconnecting } = event.detail;
      if (reconnecting) this.resetPeers();
      this.onSignalingState(state);
    });
    this.signaling.addEventListener("welcome", (event) => this.runSafely(() => this.handleWelcome(event.detail)));
    this.signaling.addEventListener("new-peer", (event) => this.runSafely(() => this.handleNewPeer(event.detail)));
    this.signaling.addEventListener("remove-peer", (event) => this.removePeer(event.detail.source));
    this.signaling.addEventListener("offer", (event) => this.runSafely(() => this.handleOffer(event.detail)));
    this.signaling.addEventListener("answer", (event) => this.runSafely(() => this.handleAnswer(event.detail)));
    this.signaling.addEventListener("candidate", (event) => this.runSafely(() => this.handleCandidate(event.detail)));
    this.signaling.addEventListener("presence", (event) => this.handlePresence(event.detail));
    this.signaling.addEventListener("raise-hand", (event) => this.handleCallStatus(event.detail, "raisedHand"));
    this.signaling.addEventListener("presenting", (event) => this.handleCallStatus(event.detail, "presenting"));
    this.signaling.addEventListener("chat", (event) => this.handleChat(event.detail));
    this.signaling.addEventListener("error", (event) => this.onStatus(event.detail.message || "Signaling error"));
  }

  async runSafely(operation) {
    try {
      await operation();
    } catch (error) {
      console.error("WebRTC signaling operation failed", error);
      this.onStatus(`Call setup error: ${error.message}`);
    }
  }

  async handleWelcome(message) {
    this.clientId = message.data.id;
    this.participants.clear();
    this.participants.set(this.clientId, {
      id: this.clientId,
      name: this.displayName,
      local: true,
      muted: !this.audioEnabled,
      videoOff: !this.videoEnabled,
      speaking: false,
      raisedHand: false,
      presenting: false
    });
    const others = message.data.peers || [];
    for (const peer of others) this.addParticipant(peer);
    this.publishParticipants();
    this.publishPresence();

    for (const peer of others) {
      if (this.shouldInitiate(peer.id)) await this.initiateOffer(peer.id);
    }
    this.onStatus(others.length ? "Negotiating media with participants…" : "Waiting for participants");
  }

  async handleNewPeer(message) {
    const peer = message.data || { id: message.source, name: "Guest" };
    peer.id = message.source;
    this.addParticipant(peer);
    this.publishParticipants();
    if (this.shouldInitiate(peer.id)) await this.initiateOffer(peer.id);
  }

  addParticipant(peer) {
    const previous = this.participants.get(peer.id) || {};
    this.participants.set(peer.id, {
      ...previous,
      ...peer,
      local: false,
      muted: previous.muted ?? false,
      videoOff: previous.videoOff ?? false,
      speaking: previous.speaking ?? false,
      raisedHand: previous.raisedHand ?? false,
      presenting: previous.presenting ?? false
    });
  }

  shouldInitiate(peerId) {
    return Boolean(this.clientId && this.clientId.localeCompare(peerId) < 0);
  }

  async initiateOffer(peerId, iceRestart = false) {
    let pc = this.peerConnections.get(peerId);
    if (pc && pc.signalingState !== "closed") {
      if (!iceRestart) return;
      if (pc.signalingState !== "stable") return;
      this.iceServers = await this.loadIceServers();
      pc.setConfiguration({ iceServers: this.iceServers });
      pc.restartIce?.();
    } else {
      pc = this.createPeerConnection(peerId);
    }
    pc.makingOffer = true;
    try {
      const offer = await pc.createOffer(iceRestart ? { iceRestart: true } : undefined);
      await pc.setLocalDescription(offer);
      this.signaling.send({
        type: "offer",
        target: peerId,
        data: { sdp: pc.localDescription.sdp }
      });
    } finally {
      pc.makingOffer = false;
    }
  }

  createPeerConnection(peerId) {
    const pc = new RTCPeerConnection({ iceServers: this.iceServers });
    this.localStream.getTracks().forEach((track) => {
      const sender = pc.addTrack(track, this.localStream);
      if (track.kind === "video") this.videoSenders.set(peerId, sender);
      if (track.kind === "audio") this.audioSenders.set(peerId, sender);
    });

    pc.addEventListener("track", (event) => {
      const stream = event.streams[0] || new MediaStream([event.track]);
      this.onRemoteStream(peerId, stream);
      if (event.track.kind === "audio") this.watchSpeaker(peerId, stream);
    });
    pc.addEventListener("icecandidate", (event) => {
      if (event.candidate) {
        this.signaling.send({
          type: "candidate",
          target: peerId,
          data: event.candidate.toJSON()
        });
      }
    });
    const reportState = () => {
      this.onConnectionState({
        peerId,
        connection: pc.connectionState,
        ice: pc.iceConnectionState
      });
      if (pc.iceConnectionState === "connected" || pc.iceConnectionState === "completed") {
        window.clearTimeout(this.restartTimers.get(peerId));
        this.restartTimers.delete(peerId);
      } else if (pc.iceConnectionState === "failed" || pc.iceConnectionState === "disconnected") {
        this.scheduleIceRestart(peerId, pc);
      }
    };
    pc.addEventListener("connectionstatechange", reportState);
    pc.addEventListener("iceconnectionstatechange", reportState);
    pc.addEventListener("negotiationneeded", () => {
      if (pc.hasNegotiated) this.runSafely(() => this.negotiate(peerId, pc));
    });

    this.peerConnections.set(peerId, pc);
    return pc;
  }

  scheduleIceRestart(peerId, pc) {
    if (!this.shouldInitiate(peerId) || this.restartTimers.has(peerId) || this.restartingPeers.has(peerId)) return;
    const delay = pc.iceConnectionState === "failed" ? 0 : 4000;
    const timer = window.setTimeout(() => {
      this.restartTimers.delete(peerId);
      if (pc.iceConnectionState !== "failed" && pc.iceConnectionState !== "disconnected") return;
      this.restartingPeers.add(peerId);
      this.runSafely(async () => {
        try {
          await this.initiateOffer(peerId, true);
        } finally {
          this.restartingPeers.delete(peerId);
        }
      });
    }, delay);
    this.restartTimers.set(peerId, timer);
  }

  async negotiate(peerId, pc) {
    if (pc.signalingState !== "stable" || pc.makingOffer) return;
    pc.makingOffer = true;
    try {
      const offer = await pc.createOffer();
      if (pc.signalingState !== "stable") return;
      await pc.setLocalDescription(offer);
      this.signaling.send({
        type: "offer",
        target: peerId,
        data: { sdp: pc.localDescription.sdp }
      });
    } finally {
      pc.makingOffer = false;
    }
  }

  async handleOffer(message) {
    const peerId = message.source;
    const pc = this.peerConnections.get(peerId) || this.createPeerConnection(peerId);
    const offerCollision = pc.makingOffer || pc.signalingState !== "stable";
    if (offerCollision && this.shouldInitiate(peerId)) return;
    if (offerCollision) {
      await pc.setLocalDescription({ type: "rollback" });
    }
    await pc.setRemoteDescription({ type: "offer", sdp: message.data.sdp });
    await this.flushCandidates(peerId, pc);
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    this.signaling.send({
      type: "answer",
      target: peerId,
      data: { sdp: pc.localDescription.sdp }
    });
    pc.hasNegotiated = true;
  }

  async handleAnswer(message) {
    const pc = this.peerConnections.get(message.source);
    if (!pc) return;
    await pc.setRemoteDescription({ type: "answer", sdp: message.data.sdp });
    await this.flushCandidates(message.source, pc);
    pc.hasNegotiated = true;
  }

  async handleCandidate(message) {
    const peerId = message.source;
    const pc = this.peerConnections.get(peerId);
    if (!pc || !pc.remoteDescription) {
      const candidates = this.pendingCandidates.get(peerId) || [];
      candidates.push(message.data);
      this.pendingCandidates.set(peerId, candidates);
      return;
    }
    await this.addCandidate(pc, message.data);
  }

  async flushCandidates(peerId, pc) {
    const candidates = this.pendingCandidates.get(peerId) || [];
    this.pendingCandidates.delete(peerId);
    for (const candidate of candidates) await this.addCandidate(pc, candidate);
  }

  async addCandidate(pc, candidate) {
    try {
      await pc.addIceCandidate(candidate);
    } catch (error) {
      console.warn("Ignoring invalid or stale ICE candidate", error);
    }
  }

  handlePresence(message) {
    const participant = this.participants.get(message.source);
    if (!participant) return;
    Object.assign(participant, message.data);
    this.publishParticipants();
  }

  handleCallStatus(message, field) {
    const participant = this.participants.get(message.source);
    if (!participant) return;
    participant[field] = Boolean(message.data?.active);
    this.publishParticipants();
  }

  handleChat(message) {
    const participant = this.participants.get(message.source);
    this.onChat({
      source: message.source,
      name: participant?.name || "Guest",
      text: message.data.text,
      sentAt: message.data.sentAt,
      mine: false
    });
  }

  sendChat(text) {
    return this.signaling.send({ type: "chat", data: { text } });
  }

  handleLocalChat(text) {
    const sent = this.sendChat(text);
    this.onChat({
      source: this.clientId,
      name: this.displayName,
      text,
      sentAt: new Date().toISOString(),
      mine: true
    });
    return sent;
  }

  handlePresenceForLocal(speaking) {
    const participant = this.participants.get(this.clientId);
    if (!participant) return;
    participant.speaking = speaking;
    this.publishParticipants();
    this.onSpeaker(this.clientId, speaking);
    this.publishPresence();
  }

  publishPresence() {
    const local = this.participants.get(this.clientId);
    if (!local) return;
    local.muted = !this.audioEnabled;
    local.videoOff = !this.videoEnabled;
    local.raisedHand = this.raisedHand;
    local.presenting = this.presenting;
    this.signaling.send({
      type: "presence",
      data: {
        muted: local.muted,
        videoOff: local.videoOff,
        speaking: local.speaking,
        raisedHand: local.raisedHand,
        presenting: local.presenting
      }
    });
    this.publishParticipants();
  }

  publishParticipants() {
    this.onParticipants([...this.participants.values()]);
  }

  toggleRaiseHand(enabled) {
    this.raisedHand = enabled;
    this.signaling.send({ type: "raise-hand", data: { active: enabled } });
    this.publishPresence();
  }

  async setAudioEnabled(enabled) {
    this.audioEnabled = enabled;
    this.localStream.getAudioTracks().forEach((track) => { track.enabled = enabled; });
    this.publishPresence();
  }

  async setVideoEnabled(enabled) {
    this.videoEnabled = enabled;
    const track = this.displayTrack || this.cameraTrack;
    if (track) track.enabled = enabled;
    this.publishPresence();
  }

  async setAudioInput(deviceId) {
    const constraints = deviceId ? { deviceId: { exact: deviceId } } : true;
    const stream = await navigator.mediaDevices.getUserMedia({ audio: constraints, video: false });
    const nextTrack = stream.getAudioTracks()[0];
    if (!nextTrack) throw new Error("The selected microphone is unavailable");
    const oldTrack = this.localStream.getAudioTracks()[0];
    for (const sender of this.audioSenders.values()) await sender.replaceTrack(nextTrack);
    if (oldTrack) {
      this.localStream.removeTrack(oldTrack);
      oldTrack.stop();
    }
    this.localStream.addTrack(nextTrack);
    nextTrack.enabled = this.audioEnabled;
    this.speakerDetectors.get("local")?.stop();
    this.speakerDetectors.delete("local");
    this.watchSpeaker("local", this.localStream);
  }

  async setCameraDevice(deviceId) {
    const video = deviceId ? { deviceId: { exact: deviceId } } : { facingMode: { ideal: this.facingMode } };
    const stream = await navigator.mediaDevices.getUserMedia({ audio: false, video });
    await this.replaceCameraTrack(stream.getVideoTracks()[0]);
  }

  async switchCamera() {
    this.facingMode = this.facingMode === "user" ? "environment" : "user";
    await this.setCameraDevice("");
  }

  async replaceCameraTrack(nextTrack) {
    if (!nextTrack) throw new Error("No camera track is available");
    const oldTrack = this.cameraTrack;
    if (oldTrack) {
      for (const sender of this.videoSenders.values()) {
        if (this.displayTrack !== sender.track) await sender.replaceTrack(nextTrack);
      }
      this.localStream.removeTrack(oldTrack);
      oldTrack.stop();
    } else {
      for (const sender of this.videoSenders.values()) await sender.replaceTrack(nextTrack);
    }
    this.cameraTrack = nextTrack;
    nextTrack.enabled = this.videoEnabled;
    this.localStream.addTrack(nextTrack);
    this.localVideo.srcObject = this.localStream;
  }

  async startScreenShare() {
    if (!navigator.mediaDevices.getDisplayMedia) throw new Error("Screen sharing is not supported by this browser");
    const stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
    const track = stream.getVideoTracks()[0];
    if (!track) throw new Error("No screen video track was provided");
    this.displayTrack = track;
    if (this.cameraTrack) this.localStream.removeTrack(this.cameraTrack);
    this.localStream.addTrack(track);
    track.enabled = this.videoEnabled;
    for (const [peerId, pc] of this.peerConnections) {
      const sender = this.videoSenders.get(peerId);
      if (sender) await sender.replaceTrack(track);
      else this.videoSenders.set(peerId, pc.addTrack(track, this.localStream));
    }
    this.localVideo.srcObject = this.localStream;
    track.addEventListener("ended", () => this.stopScreenShare());
    this.presenting = true;
    this.signaling.send({ type: "presenting", data: { active: true } });
    this.publishPresence();
  }

  async stopScreenShare() {
    if (!this.displayTrack) return;
    const displayTrack = this.displayTrack;
    this.displayTrack = null;
    this.localStream.removeTrack(displayTrack);
    displayTrack.stop();
    if (this.cameraTrack && this.cameraTrack.readyState === "live") {
      this.localStream.addTrack(this.cameraTrack);
      for (const sender of this.videoSenders.values()) await sender.replaceTrack(this.cameraTrack);
    } else {
      for (const sender of this.videoSenders.values()) await sender.replaceTrack(null);
    }
    this.localVideo.srcObject = this.localStream;
    this.presenting = false;
    this.signaling.send({ type: "presenting", data: { active: false } });
    this.publishPresence();
  }

  async setAudioOutput(deviceId, elements) {
    if (!HTMLMediaElement.prototype.setSinkId) {
      throw new Error("Changing the audio output device is not supported by this browser");
    }
    await Promise.all([...elements].map((element) => element.setSinkId(deviceId)));
  }

  watchSpeaker(peerId, stream) {
    if (this.speakerDetectors.has(peerId) || !stream.getAudioTracks().length) return;
    const AudioContextClass = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextClass) return;
    try {
      this.audioContext ||= new AudioContextClass();
      const analyser = this.audioContext.createAnalyser();
      analyser.fftSize = 512;
      const source = this.audioContext.createMediaStreamSource(stream);
      source.connect(analyser);
      const samples = new Uint8Array(analyser.fftSize);
      let lastSpeaking = false;
      let frame = 0;
      const detect = () => {
        analyser.getByteTimeDomainData(samples);
        let sum = 0;
        for (const sample of samples) {
          const value = (sample - 128) / 128;
          sum += value * value;
        }
        const speaking = Math.sqrt(sum / samples.length) > 0.055;
        if (speaking !== lastSpeaking) {
          lastSpeaking = speaking;
          if (peerId === "local") this.handlePresenceForLocal(speaking);
          else this.onSpeaker(peerId, speaking);
        }
        frame = requestAnimationFrame(detect);
      };
      this.audioContext.resume().catch((error) => console.warn("Audio activity detection is unavailable", error));
      frame = requestAnimationFrame(detect);
      this.speakerDetectors.set(peerId, {
        stop: () => {
          cancelAnimationFrame(frame);
          source.disconnect();
          analyser.disconnect();
        }
      });
    } catch (error) {
      console.warn("Could not start active-speaker detection", error);
    }
  }

  removePeer(peerId) {
    window.clearTimeout(this.restartTimers.get(peerId));
    this.restartTimers.delete(peerId);
    this.peerConnections.get(peerId)?.close();
    this.peerConnections.delete(peerId);
    this.videoSenders.delete(peerId);
    this.audioSenders.delete(peerId);
    this.pendingCandidates.delete(peerId);
    this.speakerDetectors.get(peerId)?.stop();
    this.speakerDetectors.delete(peerId);
    this.participants.delete(peerId);
    this.onRemoteStream(peerId, null);
    this.publishParticipants();
  }

  resetPeers() {
    for (const peerId of this.peerConnections.keys()) this.removePeer(peerId);
    this.participants.clear();
    this.publishParticipants();
  }

  async leave() {
    this.signaling.close();
    for (const detector of this.speakerDetectors.values()) detector.stop();
    this.speakerDetectors.clear();
    for (const pc of this.peerConnections.values()) pc.close();
    this.peerConnections.clear();
    this.videoSenders.clear();
    this.audioSenders.clear();
    this.pendingCandidates.clear();
    for (const timer of this.restartTimers.values()) window.clearTimeout(timer);
    this.restartTimers.clear();
    if (this.displayTrack) this.displayTrack.stop();
    this.localStream.getTracks().forEach((track) => track.stop());
    await this.audioContext?.close().catch((error) => console.warn("Could not close audio context", error));
    this.audioContext = null;
    this.localVideo.srcObject = null;
    this.participants.clear();
    this.isStarted = false;
  }
}
