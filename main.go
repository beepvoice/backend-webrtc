package main

import (
  "context"
  "encoding/json"
  "io"
  "io/ioutil"
  "log"
  "net/http"
  "os"
  "strings"
  "time"

  . "webrtc/backend-protobuf/go"

  "github.com/joho/godotenv"
  "github.com/julienschmidt/httprouter"
  "github.com/pion/webrtc/v2"
  "github.com/gorilla/websocket"
  "github.com/golang/protobuf/proto"
  "github.com/nats-io/go-nats"
)

// Peer config
var peerConnectionConfig webrtc.Configuration

var listen string
var natsHost string
var permissionsHost string

var upgrader websocket.Upgrader
var mediaEngine webrtc.MediaEngine
var webrtcApi *webrtc.API

var userTracks map[string] map[string] *webrtc.Track // userid + clientid
var conversationUsers map[string] []string
var userConversation map[string] string

var natsConn *nats.Conn

func main() {
  // Load .env
  err := godotenv.Load()
  if err != nil {
    log.Fatal("Error loading .env file")
  }
  listen = os.Getenv("LISTEN")
  natsHost = os.Getenv("NATS")
  permissionsHost = os.Getenv("PERMISSIONS_HOST")

  upgrader = websocket.Upgrader{}

  mediaEngine = webrtc.MediaEngine{}
  mediaEngine.RegisterCodec(webrtc.NewRTPOpusCodec(webrtc.DefaultPayloadTypeOpus, 48000))
  webrtcApi = webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

  // Read ICE servers
  fileBytes, err := ioutil.ReadFile("iceservers.txt")
  if err != nil {
    log.Fatal("error opening ice servers file")
  }
  fileString := string(fileBytes)
  servers := strings.Split(fileString, `\n`)

  peerConnectionConfig = webrtc.Configuration{
    ICEServers: []webrtc.ICEServer{
      {
        URLs: servers,
      },
    },
    SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
  }

  userTracks = make(map[string] map[string] *webrtc.Track)
  conversationUsers = make(map[string] []string)
  userConversation = make(map[string] string)

  // NATs client
  natsConn, err := nats.Connect(natsHost)
  if err != nil {
    log.Println(err)
    return
  }
  defer natsConn.Close()

  // Routes
	router := httprouter.New()
  router.GET("/connect", GetAuth(NewConnection))
  router.POST("/join/:conversationid", GetAuth(JoinConversation))

  // Start server
  log.Printf("starting server on %s", listen)
	log.Fatal(http.ListenAndServe(listen, router))
}

type RawClient struct {
  UserId string `json:"userid"`
  ClientId string `json:"clientid"`
}
func GetAuth(next httprouter.Handle) httprouter.Handle {
  return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
    ua := r.Header.Get("X-User-Claim")
    if ua == "" {
      http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
      return
    }

    var client RawClient
    err := json.Unmarshal([]byte(ua), &client)
    if err != nil {
      http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
      return
    }

    if client.UserId == "" || client.ClientId == "" {
      http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
      return
    }

    context := context.WithValue(r.Context(), "user", client)
    next(w, r.WithContext(context), p)
  }
}

func NewConnection(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
  // Get user id
  user := r.Context().Value("user").(RawClient)

  // Websocket client
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }
	defer c.Close()

  // Read SDP from websocket
	mt, msg, err := c.ReadMessage()
  if err != nil {
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  // Establish connection (it's a publisher)
  clientReceiver, err := webrtcApi.NewPeerConnection(peerConnectionConfig)
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  _, err = clientReceiver.AddTransceiver(webrtc.RTPCodecTypeAudio)
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  // Handle OnTrack
  clientReceiver.OnTrack(func(remoteTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
    rtpBuf := make([]byte, 1400)
    for {
      i, err := remoteTrack.Read(rtpBuf)
      if err != nil {
        log.Printf("%s", err)
        break
      }

      if conversationId, ok := userConversation[user.UserId]; ok {
        if users, ok2 := conversationUsers[conversationId]; ok2 {
          for _, u := range users {
            if clients, ok3 := userTracks[u]; ok3 {
              for client, track := range clients {
                if !(u == user.UserId && client == user.ClientId) {
                  _, err = track.Write(rtpBuf[:i])
                  if !(err == io.ErrClosedPipe || err == nil) {
                    log.Printf("%s", err)
                    break
                  }
                }
              }
            }
          }
        }

        start := time.Now().Unix()
        bite := Bite {
          Start: uint64(start),
          Key: conversationId,
          Data: rtpBuf[:i],
        }
        biteOut, err := proto.Marshal(&bite)
        if err != nil {
          log.Printf("%s", err)
        } else {
          natsConn.Publish("bite", biteOut)
        }

        store := Store {
          Type: "bite",
          Bite: &bite,
        }

        storeOut, err := proto.Marshal(&store)
        if err != nil {
          log.Printf("%s", err)
        } else {
          natsConn.Publish("store", storeOut)
        }
      }
    }
  })

  // Add sending track
  var track *webrtc.Track = &webrtc.Track{}
  _, err = clientReceiver.AddTrack(track)
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }
  userTracks[user.UserId][user.ClientId] = track

  // Do signalling things
  err = clientReceiver.SetRemoteDescription(
    webrtc.SessionDescription {
      SDP:  string(msg),
      Type: webrtc.SDPTypeOffer,
  })
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  answer, err := clientReceiver.CreateAnswer(nil)
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  err = clientReceiver.SetLocalDescription(answer)
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }

  err = c.WriteMessage(mt, []byte(answer.SDP))
  if err != nil {
    log.Printf("%s", err)
    http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
    return
  }
}

func JoinConversation(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
  // Get user id
  user := r.Context().Value("user").(RawClient)
  // Get conversation id
  conversationId := p.ByName("conversationid")
  if conversationId == "" {
    http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
    return
  }

  // Check permissions from backend-permissions
  response, err := http.Get(permissionsHost + "/user/" + user.UserId + "/conversation/" + conversationId)
  if err != nil {
    http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
    return
  }
  response.Body.Close()

  // Remove user from existing conversation
  if oldConversation, ok := userConversation[user.UserId]; ok {
    if users, ok2 := conversationUsers[oldConversation]; ok2 {
      var lastIndex int
      for i, u := range users {
        if u == user.UserId {
          lastIndex = i
          break;
        }
      }
      users[lastIndex] = users[len(users) - 1]
      conversationUsers[oldConversation] = users[:len(users)-1]
    }
  }

  // Populate new values
  userConversation[user.UserId] = conversationId
  if _, ok := conversationUsers[conversationId]; !ok {
    conversationUsers[conversationId] = make([]string, 0)
  }
  conversationUsers[conversationId] = append(conversationUsers[conversationId], user.UserId)

  w.WriteHeader(200)
}
