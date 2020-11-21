package core

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/geoip"
	"github.com/polevpn/netstack/tcpip/header"
	"github.com/polevpn/netstack/tcpip/transport/icmp"
	"github.com/polevpn/netstack/tcpip/transport/tcp"
	"github.com/polevpn/netstack/tcpip/transport/udp"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	POLE_CLIENT_INIT        = 0
	POLE_CLIENT_RUNING      = 1
	POLE_CLIENT_CLOSED      = 2
	POLE_CLIENT_RECONNETING = 3
)

const (
	CH_TUN_DEVICE_WRITE_SIZE     = 2048
	HEART_BEAT_INTERVAL          = 10
	WEBSOCKET_RECONNECT_TIMES    = 12
	WEBSOCKET_RECONNECT_INTERVAL = 10
	WEBSOCKET_NO_HEARTBEAT_TIMES = 4
)

const (
	CLIENT_EVENT_ADDRESS_ALLOCED = 1
	CLIENT_EVENT_RECONNECTING    = 2
	CLIENT_EVENT_STARTED         = 3
	CLIENT_EVENT_STOPPED         = 4
	CLIENT_EVENT_ERROR           = 5
	CLIENT_EVENT_RECONNECTED     = 6
)

type PoleVpnClient struct {
	tunio             *TunIO
	wsconn            *WebSocketConn
	forwarder         *LocalForwarder
	state             int
	mutex             *sync.Mutex
	endpoint          string
	user              string
	pwd               string
	sni               string
	allocip           string
	lasttimeHeartbeat time.Time
	reconnecting      bool
	wg                *sync.WaitGroup
	mode              bool
	device            *TunDevice
	handler           func(int, *PoleVpnClient, *anyvalue.AnyValue)
}

func NewPoleVpnClient() (*PoleVpnClient, error) {

	var err error

	tunio := NewTunIO(CH_TUN_DEVICE_WRITE_SIZE)

	wsconn := NewWebSocketConn()

	forwarder, err := NewLocalForwarder()

	if err != nil {
		elog.Error("forwarder create fail", err)
		return nil, err
	}

	client := &PoleVpnClient{
		tunio:     tunio,
		wsconn:    wsconn,
		forwarder: forwarder,
		state:     POLE_CLIENT_INIT,
		mutex:     &sync.Mutex{},
		wg:        &sync.WaitGroup{},
	}

	return client, nil
}

func (pc *PoleVpnClient) AttachTunDevice(device *TunDevice) {
	pc.device = device
	pc.tunio.AttachDevice(device)
	pc.tunio.StartProcess()
}

func (pc *PoleVpnClient) SetEventHandler(handler func(int, *PoleVpnClient, *anyvalue.AnyValue)) {
	pc.handler = handler
}

func (pc *PoleVpnClient) SetRouteMode(mode bool) {
	pc.mode = mode
}

func (pc *PoleVpnClient) Start(endpoint string, user string, pwd string, sni string) error {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state != POLE_CLIENT_INIT {
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "client stoped or not init"))
		}
		return errors.New("client stoped or not init")
	}

	pc.endpoint = endpoint
	pc.user = user
	pc.pwd = pwd
	pc.sni = sni

	var err error

	err = pc.wsconn.Connect(endpoint, user, pwd, "", sni)
	if err != nil {
		elog.Error("websocket connect fail", err)
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "websocket connet fail,"+err.Error()))
		}
		return err
	}

	pc.wsconn.SetHandler(CMD_ALLOC_IPADDR, pc.handlerAllocAdressRespose)
	pc.wsconn.SetHandler(CMD_S2C_IPDATA, pc.handlerIPDataResponse)
	pc.wsconn.SetHandler(CMD_CLIENT_CLOSED, pc.handlerWSConnCloseEvent)
	pc.wsconn.SetHandler(CMD_HEART_BEAT, pc.handlerHeartBeatRespose)

	pc.forwarder.SetPacketHandler(pc.handleForwarderPacket)
	pc.tunio.SetPacketHandler(pc.handleTunPacket)

	pc.wsconn.StartProcess()
	pc.forwarder.StartProcess()

	pc.AskAllocIPAddress()

	pc.lasttimeHeartbeat = time.Now()
	go pc.HeartBeat()
	pc.state = POLE_CLIENT_RUNING
	if pc.handler != nil {
		pc.handler(CLIENT_EVENT_STARTED, pc, nil)
	}
	pc.wg.Add(1)
	return nil
}

func (pc *PoleVpnClient) WaitStop() {
	pc.wg.Wait()
}

func (pc *PoleVpnClient) handleForwarderPacket(pkt []byte) {
	pc.tunio.Enqueue(pkt)
}

func (pc *PoleVpnClient) handleTunPacket(pkt []byte) {

	var err error
	ipv4pkg := header.IPv4(pkt)
	direct := false
	if ipv4pkg.Protocol() == uint8(icmp.ProtocolNumber4) {
		if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
			direct = true
		}
	} else if ipv4pkg.Protocol() == uint8(tcp.ProtocolNumber) {

		if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
			direct = true
		}
	} else if ipv4pkg.Protocol() == uint8(udp.ProtocolNumber) {
		udppkg := header.UDP(pkt[IP4_HEADER_LEN:])
		if udppkg.DestinationPort() == DNS_PORT {
			var msg dnsmessage.Message
			err = msg.Unpack(pkt[IP4_HEADER_LEN+UDP_HEADER_LEN:])
			if err != nil {
				elog.Error("parser dns packet fail", err)
			} else {
				for i := 0; i < len(msg.Questions); i++ {
					name := msg.Questions[i].Name.String()
					name = name[:len(name)-1]
					if geoip.IsDirectDomainRecursive(name) {
						elog.Debugf("DNS CN DOMAIN %v", name)
						direct = true
					}
				}
			}
		} else {
			if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
				direct = true
			}
		}

	} else {
		elog.Infof("unknown packet ip type=%v,transport type=%v", ipv4pkg.Protocol(), ipv4pkg.TransportProtocol())
		return
	}

	if pc.mode {
		direct = false
	}

	if direct {
		pc.sendIPPacketToLocalForwarder(pkt)
	} else {
		pc.sendIPPacketToRemoteWSConn(pkt)
	}

}

func (pc *PoleVpnClient) sendIPPacketToLocalForwarder(pkt []byte) {
	if pc.forwarder != nil {
		pc.forwarder.Write(pkt)
	} else {
		elog.Error("local forwarder haven't set")
	}
}

func (pc *PoleVpnClient) sendIPPacketToRemoteWSConn(pkt []byte) {

	if pc.wsconn != nil {
		buf := make([]byte, POLE_PACKET_HEADER_LEN+len(pkt))
		copy(buf[POLE_PACKET_HEADER_LEN:], pkt)
		polepkt := PolePacket(buf)
		polepkt.SetCmd(CMD_C2S_IPDATA)
		pc.wsconn.Send(polepkt)
	} else {
		elog.Error("remote ws conn haven't set")
	}

}

func (pc *PoleVpnClient) AskAllocIPAddress() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_ALLOC_IPADDR)
	pc.wsconn.Send(buf)
}

func (pc *PoleVpnClient) handlerAllocAdressRespose(pkt PolePacket, wsc *WebSocketConn) {

	av, err := anyvalue.NewFromJson(pkt.Payload())

	if err != nil {
		elog.Error("alloc address av decode fail", err)
		pc.Stop()
		return
	}

	ip1 := av.Get("ip").AsStr()

	if ip1 == "" {
		elog.Error("alloc ip fail,stop client")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "alloc ip fail"))
			return
		}
		pc.Stop()
		return
	}

	pc.allocip = ip1

	if pc.handler != nil {
		pc.handler(CLIENT_EVENT_ADDRESS_ALLOCED, pc, av)
		return
	}
}

func (pc *PoleVpnClient) handlerHeartBeatRespose(pkt PolePacket, wsc *WebSocketConn) {
	elog.Debug("received heartbeat")
	pc.lasttimeHeartbeat = time.Now()
}

func (pc *PoleVpnClient) handlerIPDataResponse(pkt PolePacket, wsc *WebSocketConn) {
	pc.tunio.Enqueue(pkt[POLE_PACKET_HEADER_LEN:])
}

func (pc *PoleVpnClient) handlerWSConnCloseEvent(pkt PolePacket, wsc *WebSocketConn) {
	elog.Info("ws client closed,start reconnect")
	pc.reconnect()
}

func (pc *PoleVpnClient) reconnect() {

	if pc.reconnecting == true {
		elog.Info("wsconn is reconnecting")
		return
	}

	pc.reconnecting = true
	pc.state = POLE_CLIENT_RECONNETING

	success := false
	for i := 0; i < WEBSOCKET_RECONNECT_TIMES; i++ {

		elog.Info("reconnecting")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_RECONNECTING, pc, nil)
		}
		err := pc.wsconn.Connect(pc.endpoint, pc.user, pc.pwd, pc.allocip, pc.sni)
		if err != nil {
			if err == ErrNetwork {
				elog.Error("reconnect fail", err)
				elog.Error("reconnect 10 seconds later")
				time.Sleep(time.Second * WEBSOCKET_RECONNECT_INTERVAL)
				continue
			} else if err == ErrIPNotExist {
				elog.Error("ip address expired,reconnect and request new")
				pc.allocip = ""
			} else {
				elog.Error("server refuse connect")
				break
			}

		} else {
			elog.Info("reconnect ok")
			if pc.allocip == "" {
				pc.AskAllocIPAddress()
			}
			pc.wsconn.StartProcess()
			pc.SendHeartBeat()
			success = true
			pc.state = POLE_CLIENT_RUNING
			if pc.handler != nil {
				pc.handler(CLIENT_EVENT_RECONNECTED, pc, nil)
			}
			break
		}
	}
	if success == false {
		elog.Error("reconnet failed,stop client")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "reconnet failed"))
		}
		pc.Stop()
	}
	pc.reconnecting = false
}

func (pc *PoleVpnClient) SendHeartBeat() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_HEART_BEAT)
	pc.wsconn.Send(buf)
}

func (pc *PoleVpnClient) HeartBeat() {

	timer := time.NewTicker(time.Second * time.Duration(HEART_BEAT_INTERVAL))

	for range timer.C {
		if pc.state == POLE_CLIENT_CLOSED {
			timer.Stop()
			break
		}
		timeNow := time.Now()
		if timeNow.Sub(pc.lasttimeHeartbeat) > time.Second*HEART_BEAT_INTERVAL*WEBSOCKET_NO_HEARTBEAT_TIMES {
			elog.Error("have not recevied heartbeat for", WEBSOCKET_NO_HEARTBEAT_TIMES, "times,close connection and reconnet")
			pc.wsconn.Close(true)
			pc.lasttimeHeartbeat = timeNow
			continue
		}
		pc.SendHeartBeat()
	}

}

func (pc *PoleVpnClient) Stop() {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state == POLE_CLIENT_CLOSED {
		elog.Error("client have been closed")
		return
	}

	pc.forwarder.Close()
	pc.wsconn.Close(false)
	pc.tunio.Close()

	pc.state = POLE_CLIENT_CLOSED

	if pc.handler != nil {
		pc.handler(CLIENT_EVENT_STOPPED, pc, nil)
	}
	pc.wg.Done()
}
