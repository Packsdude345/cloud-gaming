package session

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"

	"coordinator/app/stream"
	"coordinator/app/webrtc"
	"coordinator/app/ws"
	"coordinator/constants"
	"coordinator/pkg/socket"
	"coordinator/settings"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
)

func startVM(id string, videoRelayPort, audioRelayPort, winePort int) error {
	log.Printf("[%s] Spinning off VM\n", id)

	params := []string{
		id,
		strconv.Itoa(videoRelayPort),
		strconv.Itoa(audioRelayPort),
		strconv.Itoa(winePort),
	}
	cmd := exec.Command("./startVM.sh", params...)
	if err := cmd.Start(); err != nil {
		return err
	}

	return nil
}

func stopVM(id string) error {
	log.Printf("[%s] Stopping VM\n", id)

	cmd := exec.Command("./stopVM.sh", id)
	if err := cmd.Start(); err != nil {
		return err
	}

	return nil
}

func sendIceCandidate(wsConn *ws.Connection, candidate string) error {
	return wsConn.Send(ws.Message{
		Type: constants.IceCandidateMessage,
		Data: candidate,
	})
}

func sendOffer(wsConn *ws.Connection, offer string) error {
	return wsConn.Send(ws.Message{
		Type: constants.SDPMessage,
		Data: offer,
	})
}

func startSession(id string, wsConn *ws.Connection) (*webrtc.WebRTC, error) {
	// Create relaying streams
	videoStream := make(chan *rtp.Packet, 100)
	audioStream := make(chan *rtp.Packet, 100)
	inputStream := make(chan *webrtc.Packet, 100)

	videoListener, err := socket.NewRandomUDPListener()
	if err != nil {
		log.Printf("[%s] Couldn't create a UDP listener for video: %s\n", id, err)
		return nil, err
	}
	videoRelayPort, err := socket.ExtractPort(videoListener.LocalAddr().String())
	if err != nil {
		log.Printf("[%s] Couldn't extract UDP port for video: %s\n", id, err)
		return nil, err
	}
	audioListener, err := socket.NewRandomUDPListener()
	if err != nil {
		log.Printf("[%s] Couldn't create a UDP listener for audio: %s\n", id, err)
		return nil, err
	}
	audioRelayPort, err := socket.ExtractPort(audioListener.LocalAddr().String())
	if err != nil {
		log.Printf("[%s] Couldn't extract UDP port for audio: %s\n", id, err)
		return nil, err
	}
	wineListener, err := socket.NewRandomTCPListener()
	if err != nil {
		log.Printf("[%s] Couldn't create a TCP listener for wine: %s\n", id, err)
		return nil, err
	}
	winePort, err := socket.ExtractPort(wineListener.Addr().String())
	if err != nil {
		log.Printf("[%s] Couldn't extract TCP port for wine: %s\n", id, err)
		return nil, err
	}

	log.Printf("[%s] Wait for video at port %d\n", id, videoRelayPort)
	log.Printf("[%s] Wait for audio at port %d\n", id, audioRelayPort)
	log.Printf("[%s] Wait for syncinput at port %d\n", id, winePort)

	relayer := stream.NewStreamRelayer(id,
		videoStream, audioStream, inputStream,
		videoListener, audioListener, wineListener,
		settings.ScreenWidth, settings.ScreenHeight)
	if err := relayer.Start(); err != nil {
		fmt.Printf("[%s] Couldn't start relaying streams: %s\n", id, err)
		return nil, err
	}

	// Start VM
	if err := startVM(id, videoRelayPort, audioRelayPort, winePort); err != nil {
		log.Printf("[%s] Error when start VM: %s\n", id, err)
		return nil, err
	}

	// Start WebRTC
	webrtcConf := &webrtc.Config{
		//SinglePort:                 8443,
		//DisableDefaultInterceptors: true,
	}
	webrtcConn, err := webrtc.NewWebRTC(id,
		videoStream, audioStream, inputStream,
		webrtcConf,
	)
	if err != nil {
		return nil, err
	}

	onIceCandidateCb := func(candidate string) {
		err := sendIceCandidate(wsConn, candidate)
		if err != nil {
			log.Printf("[%s] Couldn't send candidate: %s\n", id, err)
		}
	}
	onExitCb := func() {
		log.Printf("[%s] Releasing allocated resources", id)

		if err := stopVM(id); err != nil {
			log.Printf("[%s] Error when stopping VM: %s\n", id, err)
		}

		// Must close webrtc connection first to ensure no writing to closed inputStream
		webrtcConn.StopClient()

		// Must close listeners before streams to ensure no writing to closed channels
		_ = audioListener.Close()
		_ = videoListener.Close()
		_ = wineListener.Close()

		close(videoStream)
		close(audioStream)
		close(inputStream)

		relayer.Close()
	}
	offer, err := webrtcConn.StartClient("vpx", onIceCandidateCb, onExitCb)
	if err != nil {
		fmt.Printf("[%s] Couldn't start webrtc client: %s\n", id, err)
		return nil, err
	}

	err = sendOffer(wsConn, offer)
	if err != nil {
		fmt.Printf("[%s] Couldn't send offer to WebRTC client %s", id, err)
		return nil, err
	}

	return webrtcConn, nil
}

func NewSession(w http.ResponseWriter, r *http.Request) {
	var (
		err        error
		webrtcConn *webrtc.WebRTC
	)

	sessionId := r.RemoteAddr

	conn, err := ws.NewWsConnection(w, r)
	if err != nil {
		log.Printf("[%s] Couldn't upgrade connection: %s\n", sessionId, err)
		return
	}
	defer conn.Close()

	for {
		rawMsg, err := conn.ReadText()
		if err != nil {
			if closeErr, ok := err.(*websocket.CloseError); ok && closeErr.Code == websocket.CloseGoingAway {
				return
			}

			log.Printf("[%s] Error when read WS message: %s\n", sessionId, err)
			return
		}

		var msg ws.Message
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			log.Printf("[%s] Error when parse WS message: %s\n", sessionId, err)
			continue
		}

		switch msg.Type {
		case constants.StartMessage:
			webrtcConn, err = startSession(sessionId, conn)
			if err != nil {
				log.Printf("[%s] Error when starting new session: %s\n", sessionId, err)
				webrtcConn = nil
			}
		case constants.SDPMessage:
			if webrtcConn == nil {
				continue
			}
			err := webrtcConn.SetRemoteSDP(msg.Data)
			if err != nil {
				log.Printf("[%s] Couldn't set remote SDP %s\n", sessionId, err)
				webrtcConn = nil
			}
		case constants.IceCandidateMessage:
			if webrtcConn == nil {
				continue
			}
			err := webrtcConn.AddCandidate(msg.Data)
			if err != nil {
				log.Printf("[%s] Couldn't set ICE candidate %s\n", sessionId, err)
			}
		}
	}
}
