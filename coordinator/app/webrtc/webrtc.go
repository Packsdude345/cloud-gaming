package webrtc

import (
	"coordinator/pkg/socket"
	"coordinator/utils"
	"encoding/json"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"log"
	"net"
	"sync"
	"time"
)

type WebRTC struct {
	id           string // for logging
	conn         *webrtc.PeerConnection
	imageChannel chan *rtp.Packet
	audioChannel chan *rtp.Packet
	eventChannel chan *Packet
	exitOnce     sync.Once
	inputTrack   *webrtc.DataChannel
	healthTrack  *webrtc.DataChannel
	closed       chan struct{}
}

type Packet struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type OnIceCallback func(candidate string)
type OnExitCallback func()

type PortRange struct {
	Min uint16
	Max uint16
}

type Config struct {
	SinglePort                 int
	PortRange                  PortRange
	IceIpMap                   string
	DisableDefaultInterceptors bool
}

var (
	settings    webrtc.SettingEngine
	settingOnce sync.Once
)

const MaxMissedHealthCheck int = 5

func NewWebRTC(id string, videoStream, audioStream chan *rtp.Packet, inputStream chan *Packet, conf *Config) (*WebRTC, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	i := &interceptor.Registry{}
	if !conf.DisableDefaultInterceptors {
		if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
			return nil, err
		}
	}

	settingOnce.Do(func() {
		settingEngine := webrtc.SettingEngine{}

		if conf.PortRange.Min > 0 && conf.PortRange.Max > 0 {
			if err := settingEngine.SetEphemeralUDPPortRange(conf.PortRange.Min, conf.PortRange.Max); err != nil {
				panic(err)
			}
		} else if conf.SinglePort > 0 {
			l, err := socket.NewSocketPortRoll("udp", conf.SinglePort)
			if err != nil {
				panic(err)
			}
			udpListener := l.(*net.UDPConn)
			log.Printf("[%s] Listening for WebRTC traffic at %s\n", id, udpListener.LocalAddr())
			settingEngine.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpListener))
		}

		if conf.IceIpMap != "" {
			settingEngine.SetNAT1To1IPs([]string{conf.IceIpMap}, webrtc.ICECandidateTypeHost)
		}

		settings = settingEngine
	})

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithInterceptorRegistry(i),
		webrtc.WithSettingEngine(settings),
	)

	conn, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		}},
	)
	if err != nil {
		return nil, err
	}

	return &WebRTC{
		id:           id,
		conn:         conn,
		imageChannel: videoStream,
		audioChannel: audioStream,
		eventChannel: inputStream,
		closed:       make(chan struct{}),
	}, nil
}

func (w *WebRTC) StartClient(vCodec string, iceCb OnIceCallback, exitCb OnExitCallback) (string, error) {
	log.Printf("[%s] Start WebRTC..\n", w.id)

	// Create and add video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: verbalCodecToMime(vCodec),
	}, "video", "pion")
	if err != nil {
		return "", err
	}

	_, err = w.conn.AddTrack(videoTrack)
	if err != nil {
		return "", err
	}

	// Create and add audio  track
	opusTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus,
	}, "audio", "pion")
	if err != nil {
		return "", err
	}

	_, err = w.conn.AddTrack(opusTrack)
	if err != nil {
		return "", err
	}

	err = w.addInputTrack()
	if err != nil {
		return "", err
	}

	err = w.addHealthCheck(exitCb)
	if err != nil {
		return "", err
	}

	w.conn.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			log.Printf("[%s] ICE Connected succeeded\n", w.id)
			w.startStreaming(videoTrack, opusTrack)
		}

		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateClosed || state == webrtc.ICEConnectionStateDisconnected {
			log.Printf("[%s] ICE Connected failed: %s\n", w.id, state)
			w.exitOnce.Do(exitCb)
		}
	})

	w.conn.OnICECandidate(func(iceCandidate *webrtc.ICECandidate) {
		if iceCandidate != nil {
			candidate, err := utils.EncodeBase64(iceCandidate.ToJSON())
			if err != nil {
				log.Printf("[%s] Encode IceCandidate failed: %s\n", w.id, err)
				return
			}
			iceCb(candidate)
		}
	})

	// Create offer
	offer, err := w.conn.CreateOffer(nil)
	if err != nil {
		return "", err
	}

	err = w.conn.SetLocalDescription(offer)
	if err != nil {
		return "", err
	}

	encodedOffer, err := utils.EncodeBase64(offer)
	if err != nil {
		return "", err
	}

	return encodedOffer, nil
}

func (w *WebRTC) addInputTrack() error {
	inputTrack, err := w.conn.CreateDataChannel("app-input", nil)
	if err != nil {
		return err
	}
	w.inputTrack = inputTrack

	inputTrack.OnMessage(func(rawMsg webrtc.DataChannelMessage) {
		var msg Packet
		if err := json.Unmarshal(rawMsg.Data, &msg); err != nil {
			log.Printf("[%s] Couldn't parse webrtc data message: %s\n", w.id, err)
			return
		}

		w.eventChannel <- &msg
	})
	return nil
}

func (w *WebRTC) addHealthCheck(exitCb OnExitCallback) error {
	healthTrack, err := w.conn.CreateDataChannel("health-check", nil)
	if err != nil {
		return err
	}
	w.healthTrack = healthTrack

	missedHealthCheckCounts := 0
	lock := sync.Mutex{}

	go func() {
		for {
			select {
			case <-w.closed:
				return
			default:
				lock.Lock()
				missedHealthCheckCounts += 1
				if missedHealthCheckCounts == MaxMissedHealthCheck {
					log.Printf("[%s] Health-check failed", w.id)
					w.exitOnce.Do(exitCb)
					return
				}
				lock.Unlock()
				time.Sleep(2 * time.Second)
			}
		}
	}()

	healthTrack.OnMessage(func(_ webrtc.DataChannelMessage) {
		lock.Lock()
		missedHealthCheckCounts = 0
		lock.Unlock()
	})

	return nil
}

func (w *WebRTC) SetRemoteSDP(remoteSDP string) error {
	var answer webrtc.SessionDescription

	err := utils.DecodeBase64(remoteSDP, &answer)
	if err != nil {
		log.Printf("[%s] Decode remote sdp from peer failed: %s\n", w.id, err)
		return err
	}

	err = w.conn.SetRemoteDescription(answer)
	if err != nil {
		log.Printf("[%s] Set remote description from peer failed: %s\n", w.id, err)
		return err
	}

	return nil
}

func (w *WebRTC) AddCandidate(candidate string) error {
	var iceCandidate webrtc.ICECandidateInit

	err := utils.DecodeBase64(candidate, &iceCandidate)
	if err != nil {
		log.Printf("[%s] Decode Ice candidate from peer failed: %s\n", w.id, err)
		return err
	}

	err = w.conn.AddICECandidate(iceCandidate)
	if err != nil {
		log.Printf("[%s] Add Ice candidate from peer failed: %s\n", w.id, err)
		return err
	}

	return nil
}

func (w *WebRTC) StopClient() {
	w.conn.Close()
	w.inputTrack.Close()
	w.healthTrack.Close()
	w.closed <- struct{}{}
}

func (w *WebRTC) startStreaming(videoTrack *webrtc.TrackLocalStaticRTP, opusTrack *webrtc.TrackLocalStaticRTP) {
	go func() {
		for packet := range w.imageChannel {
			if err := videoTrack.WriteRTP(packet); err != nil {
				log.Printf("[%s] Error when writing RTP to video track: %s\n", w.id, err)
			}
		}
	}()

	go func() {
		for packet := range w.audioChannel {
			if err := opusTrack.WriteRTP(packet); err != nil {
				log.Printf("[%s] Error when writing RTP to opus track: %s\n", w.id, err)
			}
		}
	}()
}

func verbalCodecToMime(vCodec string) string {
	switch vCodec {
	case "h264":
		return webrtc.MimeTypeH264
	case "vpx":
		return webrtc.MimeTypeVP8
	default:
		return webrtc.MimeTypeVP8
	}
}