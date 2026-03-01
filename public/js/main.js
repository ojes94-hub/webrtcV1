let localStream;
let wsConnection;
let clientId;

const peerConnections = {}; // peerId -> RTCPeerConnection

const localVideo = document.getElementById('localVideo');
const statusEl = document.getElementById('status');
const connectionStateEl = document.getElementById('connectionState');
const startBtn = document.getElementById('startBtn');
const hangupBtn = document.getElementById('hangupBtn');

let videoEnabled = true;
let audioEnabled = true;

function updateStatus(message) {
    statusEl.textContent = message;
    console.log(message);
}

function updateConnectionState(state) {
    connectionStateEl.textContent = `Connection: ${state}`;
}

async function startCall() {
    try {
        updateStatus('Getting camera and microphone access...');

        if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
            throw new Error('getUserMedia not supported or insecure context');
        }

        localStream = await navigator.mediaDevices.getUserMedia({
            video: {
                width: { min: 640, ideal: 1280, max: 1920 },
                height: { min: 480, ideal: 720, max: 1080 }
            },
            audio: {
                echoCancellation: true,
                noiseSuppression: true,
                autoGainControl: true
            }
        });

        localVideo.srcObject = localStream;
        updateStatus('Local media acquired');

        connectWebSocket();

        startBtn.disabled = true;
        hangupBtn.disabled = false;

    } catch (err) {
        updateStatus(`Error: ${err.message}`);
        console.error(err);
    }
}

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    wsConnection = new WebSocket(`${protocol}//${window.location.host}/ws`);

    wsConnection.onopen = () => {
        updateStatus('WebSocket connected');
        // we wait for "welcome" message which contains peers list
    };

    wsConnection.onmessage = async (event) => {
        const msg = JSON.parse(event.data);
        switch (msg.type) {
            case 'welcome':
                clientId = msg.data.id;
                updateStatus(`Joined as ${clientId}`);
                for (const pid of msg.data.peers || []) {
                    // when we ourselves join we always kick off offers to existing peers
                    await initiateOffer(pid);
                }
                break;
            case 'new-peer':
                // only one of the two peers should initiate an offer to avoid glare
                // we use a simple deterministic rule based on the peer IDs
                if (msg.source !== clientId) {
                    // if our id is smaller we'll create the offer, otherwise wait for the other side
                    if (clientId && clientId < msg.source) {
                        await initiateOffer(msg.source);
                    } else {
                        console.log('new-peer received but not initiating (order rule)', msg.source);
                    }
                }
                break;
            case 'offer':
                await handleOffer(msg.data, msg.source);
                break;
            case 'answer':
                await handleAnswer(msg.data, msg.source);
                break;
            case 'candidate':
                await handleCandidate(msg.data, msg.source);
                break;
            case 'remove-peer':
                removePeer(msg.source);
                break;
        }
    };

    wsConnection.onerror = (err) => {
        updateStatus(`WebSocket error`);
        console.error(err);
    };

    wsConnection.onclose = () => {
        updateStatus('WebSocket closed');
    };
}

function send(msg) {
    if (wsConnection && wsConnection.readyState === WebSocket.OPEN) {
        wsConnection.send(JSON.stringify(msg));
    }
}

function createPeerConnection(peerId) {
    const pc = new RTCPeerConnection({
        iceServers: [
            { urls: ['stun:stun.l.google.com:19302'] },
            { urls: ['stun:stun1.l.google.com:19302'] }
        ]
    });

    localStream.getTracks().forEach(track => pc.addTrack(track, localStream));

    pc.ontrack = (event) => {
        console.log('ontrack for', peerId, 'streams', event.streams);
        let video = document.getElementById('remote_' + peerId);
        if (!video) {
            const box = document.createElement('div');
            box.className = 'video-box';
            const label = document.createElement('h3');
            label.textContent = `Remote: ${peerId}`;
            video = document.createElement('video');
            video.id = 'remote_' + peerId;
            video.autoplay = true;
            video.playsInline = true;
            box.appendChild(label);
            box.appendChild(video);
            document.getElementById('videos').appendChild(box);
        }
        video.srcObject = event.streams[0];
    };

    pc.onicecandidate = (event) => {
        if (event.candidate) {
            send({ type: 'candidate', target: peerId, data: {
                candidate: event.candidate.candidate,
                sdpMLineIndex: event.candidate.sdpMLineIndex,
                sdpMid: event.candidate.sdpMid
            }});
        }
    };

    pc.onconnectionstatechange = () => {
        updateConnectionState(pc.connectionState);
    };

    peerConnections[peerId] = pc;
    return pc;
}

async function initiateOffer(peerId) {
    // avoid creating a second connection if one already exists
    if (peerConnections[peerId] && peerConnections[peerId].connectionState !== 'closed') {
        console.warn('offer skipped, connection already exists for', peerId);
        return;
    }

    const pc = createPeerConnection(peerId);
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    send({ type: 'offer', target: peerId, data: { sdp: offer.sdp }});
    updateStatus(`Sent offer to ${peerId}`);
}

async function handleOffer(data, source) {
    const pc = peerConnections[source] || createPeerConnection(source);
    const offerDesc = new RTCSessionDescription({ type: 'offer', sdp: data.sdp });
    await pc.setRemoteDescription(offerDesc);
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    send({ type: 'answer', target: source, data: { sdp: answer.sdp }});
    updateStatus(`Answered offer from ${source}`);
}

async function handleAnswer(data, source) {
    const pc = peerConnections[source];
    if (pc) {
        const ans = new RTCSessionDescription({ type: 'answer', sdp: data.sdp });
        await pc.setRemoteDescription(ans);
        updateStatus(`Connection established with ${source}`);
    }
}

async function handleCandidate(data, source) {
    const pc = peerConnections[source];
    if (pc) {
        const candidate = new RTCIceCandidate({
            candidate: data.candidate,
            sdpMLineIndex: data.sdpMLineIndex,
            sdpMid: data.sdpMid
        });
        await pc.addIceCandidate(candidate);
    }
}

function removePeer(peerId) {
    const pc = peerConnections[peerId];
    if (pc) {
        pc.close();
        delete peerConnections[peerId];
    }
    const elem = document.getElementById('remote_' + peerId);
    if (elem && elem.parentElement) {
        elem.parentElement.remove();
    }
}

function hangupCall() {
    updateStatus('Hanging up...');

    if (localStream) {
        localStream.getTracks().forEach(track => track.stop());
    }
    for (const id in peerConnections) {
        peerConnections[id].close();
        delete peerConnections[id];
    }
    if (wsConnection && wsConnection.readyState === WebSocket.OPEN) {
        wsConnection.close();
    }
    localVideo.srcObject = null;
    document.getElementById('videos').querySelectorAll('.video-box').forEach((box, idx) => {
        if (idx > 0) box.remove(); // keep local video
    });

    startBtn.disabled = false;
    hangupBtn.disabled = true;
    videoEnabled = true;
    audioEnabled = true;
    document.getElementById('toggleVideoBtn').textContent = 'Camera Off';
    document.getElementById('toggleAudioBtn').textContent = 'Mic Off';
    updateStatus('Call ended');
    updateConnectionState('Disconnected');
}

function toggleVideo() {
    if (!localStream) return;
    const videoToggleBtn = document.getElementById('toggleVideoBtn');
    localStream.getVideoTracks().forEach(track => track.enabled = !(track.enabled));
    videoEnabled = localStream.getVideoTracks()[0]?.enabled;
    videoToggleBtn.textContent = videoEnabled ? 'Camera Off' : 'Camera On';
    videoToggleBtn.style.background = videoEnabled ? '#ffa726' : '#ef5350';
}

function toggleAudio() {
    if (!localStream) return;
    const audioToggleBtn = document.getElementById('toggleAudioBtn');
    localStream.getAudioTracks().forEach(track => track.enabled = !(track.enabled));
    audioEnabled = localStream.getAudioTracks()[0]?.enabled;
    audioToggleBtn.textContent = audioEnabled ? 'Mic Off' : 'Mic On';
    audioToggleBtn.style.background = audioEnabled ? '#ffa726' : '#ef5350';
}
