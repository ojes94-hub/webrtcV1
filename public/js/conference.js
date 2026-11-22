const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

export class ConferenceSession {
  constructor(options) {
    this.roomId = options.roomId;
    this.displayName = options.displayName;
    this.localVideo = options.localVideo;
    this.localStream = options.localStream || null;
    this.onStatus = options.onStatus;
    this.onSignalingState = options.onSignalingState;
    this.onConnectionState = options.onConnectionState;
    this.onParticipants = options.onParticipants;
    this.onRemoteStream = options.onRemoteStream;
    this.onChat = options.onChat;
    this.onSpeaker = options.onSpeaker;
    this.participantStates = new Map();
    this.remoteTracks = new Map();
    this.cameraTrack = this.localStream?.getVideoTracks()[0] || null;
    this.audioTrack = this.localStream?.getAudioTracks()[0] || null;
    this.displayTrack = null;
    this.facingMode = "user";
    this.raisedHand = false;
    this.presenting = false;
    this.audioEnabled = Boolean(this.audioTrack?.enabled);
    this.videoEnabled = Boolean(this.cameraTrack?.enabled);
    this.room = null;
    this.clientId = "";
  }

  async start() {
    if (!window.LivekitClient?.Room) throw new Error("Conference calling SDK could not be loaded");
    const response = await fetch("/api/conference-token", {
      method: "POST",
      cache: "no-store",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ room: this.roomId, username: this.displayName })
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || `Conference token request failed (${response.status})`);
    }
    const { token, url } = await response.json();
    if (!token || !url) throw new Error("Conference token response is incomplete");

    this.room = new window.LivekitClient.Room({ adaptiveStream: true, dynacast: true });
    this.bindEvents();
    this.onSignalingState("connecting");
    await this.room.connect(url, token, { autoSubscribe: true });
    this.clientId = this.room.localParticipant.identity;
    this.localVideo.srcObject = this.localStream;
    for (const track of this.localStream?.getTracks() || []) {
      if (track.kind === "video") this.cameraTrack = track;
      if (track.kind === "audio") this.audioTrack = track;
      await this.room.localParticipant.publishTrack(track, {
        source: track.kind === "video" ? window.LivekitClient.Track.Source.Camera : window.LivekitClient.Track.Source.Microphone,
        simulcast: track.kind === "video"
      });
    }
    this.onSignalingState("connected");
    this.onStatus("Connected to conference");
    this.publishParticipants();
  }

  bindEvents() {
    const LK = window.LivekitClient;
    this.room.on(LK.RoomEvent.ParticipantConnected, () => this.publishParticipants());
    this.room.on(LK.RoomEvent.ParticipantDisconnected, (participant) => {
      this.remoteTracks.delete(participant.identity);
      this.onRemoteStream(participant.identity, null);
      this.participantStates.delete(participant.identity);
      this.publishParticipants();
    });
    this.room.on(LK.RoomEvent.TrackSubscribed, (track, publication, participant) => {
      const tracks = this.remoteTracks.get(participant.identity) || new Map();
      tracks.set(publication.trackSid, track.mediaStreamTrack);
      this.remoteTracks.set(participant.identity, tracks);
      this.onRemoteStream(participant.identity, new MediaStream([...tracks.values()]));
      this.publishParticipants();
      if (publication.source === LK.Track.Source.ScreenShare) {
        this.setParticipantState(participant.identity, { presenting: true });
      }
    });
    this.room.on(LK.RoomEvent.TrackUnsubscribed, (track, publication, participant) => {
      const tracks = this.remoteTracks.get(participant.identity);
      tracks?.delete(publication.trackSid);
      if (tracks?.size) this.onRemoteStream(participant.identity, new MediaStream([...tracks.values()]));
      else {
        this.remoteTracks.delete(participant.identity);
        this.onRemoteStream(participant.identity, null);
      }
      if (publication.source === LK.Track.Source.ScreenShare) {
        this.setParticipantState(participant.identity, { presenting: false });
      }
      this.publishParticipants();
    });
    this.room.on(LK.RoomEvent.ActiveSpeakersChanged, () => {
      this.onSpeaker(this.clientId, Boolean(this.room.localParticipant.isSpeaking));
      for (const participant of this.room.remoteParticipants.values()) {
        this.onSpeaker(participant.identity, Boolean(participant.isSpeaking));
      }
      this.publishParticipants();
    });
    this.room.on(LK.RoomEvent.DataReceived, (payload, participant, kind, topic) => {
      if (topic !== "meet-chat" && topic !== "meet-state") return;
      let data;
      try {
        data = JSON.parse(textDecoder.decode(payload));
      } catch {
        this.onStatus("Received invalid conference data");
        return;
      }
      if (topic === "meet-chat" && typeof data.text === "string") {
        this.onChat({
          source: participant?.identity || "",
          name: participant?.name || participant?.identity || "Guest",
          text: data.text,
          sentAt: data.sentAt || new Date().toISOString(),
          mine: false
        });
      } else if (topic === "meet-state" && participant) {
        this.setParticipantState(participant.identity, data);
      }
    });
    this.room.on(LK.RoomEvent.Reconnecting, () => this.onSignalingState("reconnecting"));
    this.room.on(LK.RoomEvent.Reconnected, () => this.onSignalingState("connected"));
    this.room.on(LK.RoomEvent.Disconnected, () => this.onSignalingState("disconnected"));
  }

  setParticipantState(identity, state) {
    this.participantStates.set(identity, { ...(this.participantStates.get(identity) || {}), ...state });
    this.publishParticipants();
  }

  participants() {
    if (!this.room) return [];
    const local = this.room.localParticipant;
    const peers = [...this.room.remoteParticipants.values()];
    return [local, ...peers].map((participant) => {
      const state = this.participantStates.get(participant.identity) || {};
      const isLocal = participant === local;
      return {
        id: participant.identity,
        name: participant.name || participant.identity,
        local: isLocal,
        muted: isLocal ? !this.audioEnabled : ![...participant.audioTrackPublications.values()].some((pub) => pub.isSubscribed && !pub.isMuted),
        videoOff: isLocal ? !this.videoEnabled : ![...participant.videoTrackPublications.values()].some((pub) => pub.isSubscribed && !pub.isMuted),
        speaking: Boolean(participant.isSpeaking),
        raisedHand: isLocal ? this.raisedHand : Boolean(state.raisedHand),
        presenting: isLocal ? this.presenting : Boolean(state.presenting)
      };
    });
  }

  publishParticipants() {
    this.onParticipants(this.participants());
  }

  async sendData(data, topic) {
    if (!this.room) return false;
    await this.room.localParticipant.publishData(textEncoder.encode(JSON.stringify(data)), {
      reliable: true,
      topic
    });
    return true;
  }

  async publishLocalState() {
    const state = {
      muted: !this.audioEnabled,
      videoOff: !this.videoEnabled,
      raisedHand: this.raisedHand,
      presenting: this.presenting
    };
    if (this.clientId) this.setParticipantState(this.clientId, state);
    await this.sendData(state, "meet-state");
  }

  async handleLocalChat(text) {
    try {
      const sent = await this.sendData({ text, sentAt: new Date().toISOString() }, "meet-chat");
      this.onChat({ source: this.clientId, name: this.displayName, text, sentAt: new Date().toISOString(), mine: true });
      return sent;
    } catch (error) {
      this.onStatus(`Chat not sent: ${error.message}`);
      return false;
    }
  }

  async toggleRaiseHand(enabled) {
    this.raisedHand = enabled;
    await this.publishLocalState();
    this.publishParticipants();
  }

  async setAudioEnabled(enabled) {
    if (enabled && !this.audioTrack) {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: false });
      const track = stream.getAudioTracks()[0];
      if (!track) throw new Error("Microphone unavailable");
      this.localStream ||= new MediaStream();
      this.localStream.addTrack(track);
      this.audioTrack = track;
      await this.room.localParticipant.publishTrack(track, { source: window.LivekitClient.Track.Source.Microphone });
    }
    this.audioEnabled = enabled;
    await this.setTrackEnabled(this.audioTrack, enabled);
    await this.publishLocalState();
    this.publishParticipants();
  }

  async setVideoEnabled(enabled) {
    if (enabled && !this.cameraTrack) {
      const stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: { ideal: this.facingMode } }, audio: false });
      const track = stream.getVideoTracks()[0];
      if (!track) throw new Error("Camera unavailable");
      this.localStream ||= new MediaStream();
      this.localStream.addTrack(track);
      this.cameraTrack = track;
      this.localVideo.srcObject = this.localStream;
      await this.room.localParticipant.publishTrack(track, {
        source: window.LivekitClient.Track.Source.Camera,
        simulcast: true
      });
    }
    this.videoEnabled = enabled;
    const track = this.displayTrack || this.cameraTrack;
    await this.setTrackEnabled(track, enabled);
    await this.publishLocalState();
    this.publishParticipants();
  }

  async setTrackEnabled(mediaTrack, enabled) {
    if (!mediaTrack) return;
    const publication = [...this.room.localParticipant.trackPublications.values()]
      .find((item) => item.track?.mediaStreamTrack === mediaTrack);
    if (publication?.track) {
      if (enabled) await publication.track.unmute();
      else await publication.track.mute();
    } else {
      mediaTrack.enabled = enabled;
    }
  }

  async replaceLocalTrack(oldTrack, nextTrack, source, simulcast = false) {
    const publication = [...this.room.localParticipant.trackPublications.values()]
      .find((item) => item.track?.mediaStreamTrack === oldTrack);
    if (publication) {
      await this.room.localParticipant.unpublishTrack(publication.track);
    }
    this.localStream ||= new MediaStream();
    if (oldTrack) {
      this.localStream.removeTrack(oldTrack);
      oldTrack.stop();
    }
    this.localStream.addTrack(nextTrack);
    nextTrack.enabled = source === window.LivekitClient.Track.Source.Camera ? this.videoEnabled : this.audioEnabled;
    await this.room.localParticipant.publishTrack(nextTrack, { source, simulcast });
    this.localVideo.srcObject = this.localStream;
  }

  async setAudioInput(deviceId) {
    const stream = await navigator.mediaDevices.getUserMedia({
      audio: deviceId ? { deviceId: { exact: deviceId } } : true,
      video: false
    });
    const track = stream.getAudioTracks()[0];
    if (!track) throw new Error("The selected microphone is unavailable");
    const oldTrack = this.audioTrack;
    await this.replaceLocalTrack(oldTrack, track, window.LivekitClient.Track.Source.Microphone);
    this.audioTrack = track;
  }

  async setCameraDevice(deviceId) {
    const stream = await navigator.mediaDevices.getUserMedia({
      video: deviceId ? { deviceId: { exact: deviceId } } : { facingMode: { ideal: this.facingMode } },
      audio: false
    });
    const track = stream.getVideoTracks()[0];
    if (!track) throw new Error("No camera track is available");
    const oldTrack = this.cameraTrack;
    await this.replaceLocalTrack(oldTrack, track, window.LivekitClient.Track.Source.Camera, true);
    this.cameraTrack = track;
  }

  async switchCamera() {
    this.facingMode = this.facingMode === "user" ? "environment" : "user";
    await this.setCameraDevice("");
  }

  async startScreenShare() {
    if (!navigator.mediaDevices.getDisplayMedia) throw new Error("Screen sharing is not supported by this browser");
    const stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
    const track = stream.getVideoTracks()[0];
    if (!track) throw new Error("No screen video track was provided");
    const camera = this.cameraTrack;
    const publication = [...this.room.localParticipant.videoTrackPublications.values()]
      .find((item) => item.source === window.LivekitClient.Track.Source.Camera);
    if (publication) await this.room.localParticipant.unpublishTrack(publication.track, false);
    this.displayTrack = track;
    this.localStream ||= new MediaStream();
    if (camera) this.localStream.removeTrack(camera);
    this.localStream.addTrack(track);
    await this.room.localParticipant.publishTrack(track, { source: window.LivekitClient.Track.Source.ScreenShare });
    this.localVideo.srcObject = this.localStream;
    this.presenting = true;
    await this.publishLocalState();
    track.addEventListener("ended", () => this.stopScreenShare());
  }

  async stopScreenShare() {
    if (!this.displayTrack) return;
    const track = this.displayTrack;
    this.displayTrack = null;
    const publication = [...this.room.localParticipant.videoTrackPublications.values()]
      .find((item) => item.source === window.LivekitClient.Track.Source.ScreenShare);
    if (publication) await this.room.localParticipant.unpublishTrack(publication.track);
    this.localStream?.removeTrack(track);
    track.stop();
    if (this.cameraTrack?.readyState === "live") {
      this.localStream?.addTrack(this.cameraTrack);
      await this.room.localParticipant.publishTrack(this.cameraTrack, {
        source: window.LivekitClient.Track.Source.Camera,
        simulcast: true
      });
    }
    this.localVideo.srcObject = this.localStream;
    this.presenting = false;
    await this.publishLocalState();
  }

  async setAudioOutput(deviceId, elements) {
    if (typeof HTMLMediaElement.prototype.setSinkId !== "function") return;
    await Promise.all([...elements].map((element) => element.setSinkId(deviceId)));
  }

  async leave() {
    await this.room?.disconnect();
    for (const tracks of this.remoteTracks.values()) {
      tracks.forEach((track) => track.stop());
    }
    this.remoteTracks.clear();
    this.localStream?.getTracks().forEach((track) => track.stop());
    this.localStream = null;
    if (this.localVideo) this.localVideo.srcObject = null;
  }
}
