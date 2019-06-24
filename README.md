# backend-webrtc

Beep backend handling WebRTC Selective Forwarding Units (SFUs). 

**The security of this service is handled by backend-auth called by traefik.**

## Environment variables

Supply environment variables by either exporting them or editing `.env`.

| ENV | Description | Default |
| --- | ----------- | ------- |
| LISTEN | Host and port to listen on | :80 |

## API

All endpoints require a populated `X-User-Claim` header from `backend-auth`.

| Contents |
| -------- |
| New Connection |
| Join Conversation |

---

### New Connection

```
GET /connect
```

Creates a new WebRTC peer connection with the server. Connection gets upgraded to a websocket through which signalling occurs before it is closed once the peer connection is established. Please supply the token via GET querystring since Websockets do not support Auth headers.

#### Example (Javascript)

```js
const wsuri = `wss:\/\/localhost:80/connect`;

let localSDP = '';
let remoteSDP = '';

const socket = new WebSocket(wsuri);
socket.onmessage = (e) => {
  remoteSDP = e.data;
  try {
    pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: remoteSDP });
  } catch(e) {
    console.error(e);
  }
};

const pc = new RTCPeerConnection({
  iceServers: [
    {
      urls: 'stun:stun.l.google.com:19302',
    },
  ],
});
pc.onicecandidate = (e) => {
  if (e.candidate === null) {
    localSDP = pc.localDescription.sdp;
    socket.send(localSDP);
  }
};

navigator.mediaDevices
         .getUserMedia({ audio: true })
         .then((stream) => {
           for (const track of stream.getAudioTracks()) {
             pc.addTrack(track);
           }
           pc.createOffer()
             .then((d) => {
               p.setLocalDescription(d);
             })
             .catch((e) => console.error(e));
         })
         .catch((e) => console.error(e));

pc.ontrack = (event) => {
  // Do stuff with event.streams
};
```

#### Errors

| Code | Description |
| ---- | ----------- |
| 400 | Error parsing `X-User-Claims` header |
| 500 | Error establishing WebRTC connection |

---

### Join Conversation

```
POST /join/:conversationid
```

Signify a user's intention to join a conversation.

#### Params

| Name | Type | Description |
| ---- | ---- | ----------- |
| conversationid | String | ID of conversation to be joined |

#### Success (200 OK)

Empty body

#### Errors

| Code | Description |
| ---- | ----------- |
| 400 | Error parsing `X-User-Claims` header |
