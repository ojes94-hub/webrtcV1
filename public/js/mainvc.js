let localStream;
let wsConnection;
let clientId;

const peerConnections = {}; // peerId -> RTCPeerConnection

const localVideo = document.getElementById('localVideo');
const statusEl = document.getElementById('status');
const connectionStateEl = document.getElementById('connectionState');

// New UI elements (start/hangup/audio/video buttons are icons)
const startCallBtn = document.getElementById('startCallBtn');
const hangupBtn = document.getElementById('hangupBtn');
const toggleAudioBtn = document.getElementById('toggleAudioBtn');
const toggleVideoBtn = document.getElementById('toggleVideoBtn');

let videoEnabled = true;
let audioEnabled = true;
let currentZoom = 1;

function updateStatus(message) {
    statusEl.textContent = message;
    console.log(message);
}

function updateConnectionState(state) {
    connectionStateEl.textContent = `Connection: ${state}`;
}

const zoomInOneStep = async () => {
    if (!localStream) {
        console.warn('No local stream available');
        return;
    }
    const [track] = localStream.getVideoTracks();
    if (!track) return;

    const capabilities = track.getCapabilities();
    if (!capabilities.zoom) {
        updateStatus('Zoom not supported on this device');
        return;
    }

    const step = capabilities.zoom.step || 0.1;
    const maxZoom = capabilities.zoom.max || 4;
    const newZoom = Math.min(maxZoom, currentZoom + step);

    try {
        await track.applyConstraints({
            advanced: [{ zoom: newZoom }]
        });
        currentZoom = newZoom;
        updateStatus(`Zoom: ${newZoom.toFixed(2)}x`);
    } catch (err) {
        console.error('Zoom in failed:', err);
        updateStatus('Zoom in failed');
    }
};

const zoomOutOneStep = async () => {
    if (!localStream) {
        console.warn('No local stream available');
        return;
    }
    const [track] = localStream.getVideoTracks();
    if (!track) return;

    const capabilities = track.getCapabilities();
    if (!capabilities.zoom) {
        updateStatus('Zoom not supported on this device');
        return;
    }

    const step = capabilities.zoom.step || 0.1;
    const minZoom = capabilities.zoom.min || 1;
    const newZoom = Math.max(minZoom, currentZoom - step);

    try {
        await track.applyConstraints({
            advanced: [{ zoom: newZoom }]
        });
        currentZoom = newZoom;
        updateStatus(`Zoom: ${newZoom.toFixed(2)}x`);
    } catch (err) {
        console.error('Zoom out failed:', err);
        updateStatus('Zoom out failed');
    }
};
async function startCall() {
    try {
        updateStatus('Getting camera and microphone access...');

        if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
            throw new Error('getUserMedia not supported or insecure context');
        }
    const screenRatio = window.innerWidth / window.innerHeight;

    const constraints = {
    video: {
        width: { min: 640, ideal: 1280, max: 1920 },
        height: { min: 480, ideal: 720, max: 1080 },
        // This forces the stream to try and match the screen's shape
        aspectRatio: { ideal: screenRatio }
    },
    audio: {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true
    }
};

    localStream = await navigator.mediaDevices.getUserMedia(constraints);
        // localStream = await navigator.mediaDevices.getUserMedia({
        //     video: {
        //         width: { min: 640, ideal: 1280, max: 1920 },
        //         height: { min: 480, ideal: 720, max: 1080 }
        //     },
        //     audio: {
        //         echoCancellation: true,
        //         noiseSuppression: true,
        //         autoGainControl: true
        //     }
        // });

        localVideo.srcObject = localStream;
        updateStatus('Local media acquired');
        // Remove fullscreen-init class after call starts
        const grid = document.getElementById('videos');
        if (grid.classList.contains('fullscreen-init')) {
            grid.classList.remove('fullscreen-init');
        }
        adjustLayout();

        connectWebSocket();

        if (startCallBtn) startCallBtn.disabled = true;
        if (hangupBtn) hangupBtn.disabled = false;
        if (toggleAudioBtn) toggleAudioBtn.disabled = false;
        if (toggleVideoBtn) toggleVideoBtn.disabled = false;

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

function pinTile(tile) {
    const grid = document.querySelector('.video-grid');
    const currentHost = document.getElementById('tile-susan');
    if (currentHost && currentHost !== tile) {
        currentHost.classList.remove('is-minimized');
        currentHost.style.top = ""; currentHost.style.left = "";
        currentHost.style.bottom = ""; currentHost.style.right = "";
        currentHost.onmousedown = null;
        const icon = currentHost.querySelector('.minimize-btn [data-lucide]');
        if (icon) icon.setAttribute('data-lucide', 'minimize-2');
        currentHost.id = '';
    }
    grid.scrollTo({ top: 0, behavior: 'smooth' });
    tile.id = 'tile-susan';
    if (document.getElementById('people-section')?.classList.contains('active')) {
        updatePeopleList();
    }
    lucide.createIcons();
}

function adjustLayout() {
    const grid = document.querySelector('.video-grid');
    const boxes = grid.querySelectorAll('.video-box');
    const count = boxes.length;
    grid.classList.remove('single','two','many');

    const localTile = document.getElementById('tile-susan');

    if (count === 1) {
        grid.classList.add('single');
        // ensure local is pinned and full size
        if (localTile) {
            localTile.id = 'tile-susan';
            localTile.classList.remove('is-minimized');
        }
    } else if (count === 2) {
        grid.classList.add('two');
        // pin remote (assume second box is remote)
        boxes.forEach(box => {
            const tile = box.querySelector('.video-tile');
            if (tile && tile.id !== 'tile-susan') {
                pinTile(tile);
            }
        });
        // minimize local
        if (localTile) {
            localTile.classList.add('is-minimized');
            const icon = localTile.querySelector('.minimize-btn [data-lucide]');
            if (icon) icon.setAttribute('data-lucide', 'maximize-2');
            startDraggable(localTile);
        }
    } else {
        grid.classList.add('many');
        // Responsive grid: twos in a row for mobile, tiles for desktop
        // CSS handles layout via .many class and media queries
        boxes.forEach(box => {
            const tile = box.querySelector('.video-tile');
            if (tile) {
                tile.id = '';
                tile.classList.remove('is-minimized');
                tile.onmousedown = null;
                tile.style.top = '';
                tile.style.left = '';
                tile.style.bottom = '';
                tile.style.right = '';
                const icon = tile.querySelector('.minimize-btn [data-lucide]');
                if (icon) icon.setAttribute('data-lucide', 'minimize-2');
            }
        });
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
            // build a tile matching the local video structure for consistent styling
            const box = document.createElement('div');
            box.className = 'video-box';
            box.style.width = '100%';
            box.style.height = '100%';

            const tile = document.createElement('div');
            tile.className = 'video-tile';
            tile.id = 'tile_' + peerId;
            tile.style.width = '100%';
            tile.style.height = '100%';

            // minimize button (visible only when pinned via CSS rules)
            const minimizeBtn = document.createElement('button');
            minimizeBtn.className = 'minimize-btn';
            minimizeBtn.addEventListener('click', (e) => toggleMinimize(e));
            minimizeBtn.innerHTML = '<i data-lucide="minimize-2"></i>';
            tile.appendChild(minimizeBtn);

            video = document.createElement('video');
            video.className = 'video';
            video.id = 'remote_' + peerId;
            video.autoplay = true;
            video.playsInline = true;
            // ensure the element fills the tile and uses cover mode for responsiveness
            video.style.width = '100%';
            video.style.height = '100%';
            video.style.objectFit = 'cover';
            tile.appendChild(video);

            const muteIndicator = document.createElement('div');
            muteIndicator.className = 'mute-indicator';
            muteIndicator.innerHTML = '<i data-lucide="mic-off"></i>';
            tile.appendChild(muteIndicator);

            const overlay = document.createElement('div');
            overlay.className = 'tile-overlay';
            overlay.innerHTML = `<span>Remote: ${peerId}</span><i data-lucide="mic"></i>`;
            tile.appendChild(overlay);

            box.appendChild(tile);
            document.getElementById('videos').appendChild(box);

            // refresh icons after adding new elements
            if (typeof lucide !== 'undefined' && lucide.createIcons) lucide.createIcons();
            adjustLayout();
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
    adjustLayout();
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
    adjustLayout();

    if (startCallBtn) startCallBtn.disabled = false;
    if (hangupBtn) hangupBtn.disabled = true;
    if (toggleAudioBtn) toggleAudioBtn.disabled = true;
    if (toggleVideoBtn) toggleVideoBtn.disabled = true;
    videoEnabled = true;
    audioEnabled = true;
    currentZoom = 1;
    updateStatus('Call ended');
    updateConnectionState('Disconnected');
}

function toggleVideo() {
    if (!localStream) return;
    localStream.getVideoTracks().forEach(track => track.enabled = !(track.enabled));
    videoEnabled = localStream.getVideoTracks()[0]?.enabled;
    updateStatus(videoEnabled ? 'Video enabled' : 'Video disabled');
}

function toggleAudio() {
    if (!localStream) return;
    localStream.getAudioTracks().forEach(track => track.enabled = !(track.enabled));
    audioEnabled = localStream.getAudioTracks()[0]?.enabled;
    updateStatus(audioEnabled ? 'Microphone unmuted' : 'Microphone muted');
}

// --- UI initialization and event binding ---
document.addEventListener('DOMContentLoaded', () => {
    if (startCallBtn) startCallBtn.addEventListener('click', startCall);
    if (hangupBtn) hangupBtn.addEventListener('click', hangupCall);

    // initial button states
    if (hangupBtn) hangupBtn.disabled = true;
    if (toggleAudioBtn) toggleAudioBtn.disabled = true;
    if (toggleVideoBtn) toggleVideoBtn.disabled = true;
});
