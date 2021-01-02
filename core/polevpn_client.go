package core

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/geoip"
	"github.com/polevpn/netstack/tcpip/header"
	"github.com/polevpn/netstack/tcpip/transport/icmp"
	"github.com/polevpn/netstack/tcpip/transport/tcp"
	"github.com/polevpn/netstack/tcpip/transport/udp"
	"github.com/polevpn/xnet/dns/dnsmessage"
)

const (
	POLE_CLIENT_INIT        = 0
	POLE_CLIENT_RUNING      = 1
	POLE_CLIENT_CLOSED      = 2
	POLE_CLIENT_RECONNETING = 3
)

const (
	VERSION_IP_V4                = 4
	VERSION_IP_V6                = 6
	TUN_DEVICE_CH_WRITE_SIZE     = 2048
	HEART_BEAT_INTERVAL          = 10
	RECONNECT_TIMES              = 60
	RECONNECT_INTERVAL           = 5
	WEBSOCKET_NO_HEARTBEAT_TIMES = 3
)

const (
	CLIENT_EVENT_ADDRESS_ALLOCED = 1
	CLIENT_EVENT_RECONNECTING    = 2
	CLIENT_EVENT_STARTED         = 3
	CLIENT_EVENT_STOPPED         = 4
	CLIENT_EVENT_ERROR           = 5
	CLIENT_EVENT_RECONNECTED     = 6
)

const (
	ERROR_LOGIN   = "login"
	ERROR_NETWORK = "network"
	ERROR_UNKNOWN = "unknown"
	ERROR_IO      = "io"
	ERROR_ALLOC   = "alloc"
)

var plog *elog.EasyLogger

type PoleVpnClient struct {
	tunio             *TunIO
	conn              Conn
	forwarder         *LocalForwarder
	state             int
	mutex             *sync.Mutex
	endpoint          string
	user              string
	pwd               string
	allocip           string
	localip           string
	lasttimeHeartbeat time.Time
	reconnecting      bool
	wg                *sync.WaitGroup
	mode              bool
	device            *TunDevice
	handler           func(int, *PoleVpnClient, *anyvalue.AnyValue)
	host              string
}

func init() {
	if plog == nil {
		plog = elog.GetLogger()
	}
}

func SetLogger(elog *elog.EasyLogger) {
	plog = elog
}

func NewPoleVpnClient() (*PoleVpnClient, error) {

	var err error

	forwarder, err := NewLocalForwarder()

	if err != nil {
		plog.Error("forwarder create fail", err)
		return nil, err
	}

	client := &PoleVpnClient{
		conn:      nil,
		forwarder: forwarder,
		state:     POLE_CLIENT_INIT,
		mutex:     &sync.Mutex{},
		wg:        &sync.WaitGroup{},
	}
	return client, nil
}

func (pc *PoleVpnClient) AttachTunDevice(device *TunDevice) {
	pc.device = device
	if pc.tunio != nil {
		pc.tunio.Close()
	}

	pc.tunio = NewTunIO(TUN_DEVICE_CH_WRITE_SIZE)
	pc.tunio.SetPacketHandler(pc.handleTunPacket)
	pc.tunio.AttachDevice(device)
	pc.tunio.StartProcess()
}

func (pc *PoleVpnClient) SetEventHandler(handler func(int, *PoleVpnClient, *anyvalue.AnyValue)) {
	pc.handler = handler
}

func (pc *PoleVpnClient) SetRouteMode(mode bool) {
	pc.mode = mode
}

func (pc *PoleVpnClient) Start(endpoint string, user string, pwd string) error {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state != POLE_CLIENT_INIT {
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "client stoped or not init").Set("type", ERROR_UNKNOWN))
		}
		return errors.New("client stoped or not init")
	}

	pc.user = user
	pc.pwd = pwd

	var err error

	u, err := url.Parse(endpoint)

	pc.host = u.Host

	if strings.HasPrefix(endpoint, "wss://") || strings.HasPrefix(endpoint, "ws://") {
		pc.conn = NewWebSocketConn()
	} else if strings.HasPrefix(endpoint, "h2s://") || strings.HasPrefix(endpoint, "h2://") {
		if strings.HasPrefix(endpoint, "h2s://") {
			endpoint = strings.Replace(endpoint, "h2s://", "https://", -1)
		}
		if strings.HasPrefix(endpoint, "h2://") {
			endpoint = strings.Replace(endpoint, "h2://", "http://", -1)
		}
		pc.conn = NewHttp2Conn()
	} else if strings.HasPrefix(endpoint, "kcp://") {
		endpoint = strings.Replace(endpoint, "kcp://", "", -1)
		pc.conn = NewKCPConn()
	} else {
		pc.conn = NewHttpConn()
	}

	pc.conn.SetLocalIP(pc.localip)
	pc.endpoint = endpoint

	err = pc.conn.Connect(endpoint, user, pwd, "")
	if err != nil {
		if err == ErrLoginVerify {
			if pc.handler != nil {
				pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "user or password invalid").Set("type", ERROR_LOGIN))
			}
		} else {
			if pc.handler != nil {
				pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "connet fail,"+err.Error()).Set("type", ERROR_NETWORK))
			}
		}
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_STOPPED, pc, nil)
		}
		return err
	}

	pc.conn.SetHandler(CMD_ALLOC_IPADDR, pc.handlerAllocAdressRespose)
	pc.conn.SetHandler(CMD_S2C_IPDATA, pc.handlerIPDataResponse)
	pc.conn.SetHandler(CMD_CLIENT_CLOSED, pc.handlerConnCloseEvent)
	pc.conn.SetHandler(CMD_HEART_BEAT, pc.handlerHeartBeatRespose)

	pc.forwarder.SetPacketHandler(pc.handleForwarderPacket)

	pc.conn.StartProcess()
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

func (pc *PoleVpnClient) SetLocalIP(ip string) {
	pc.forwarder.SetLocalIP(ip)
	if pc.conn != nil {
		pc.conn.SetLocalIP(ip)
	}
	pc.localip = ip
}

func (pc *PoleVpnClient) CloseConnect(flag bool) {
	pc.conn.Close(flag)
	pc.forwarder.ClearConnect()
}

func (pc *PoleVpnClient) WaitStop() {
	pc.wg.Wait()
}

func (pc *PoleVpnClient) handleForwarderPacket(pkt []byte) {
	pc.tunio.Enqueue(pkt)
}

func (pc *PoleVpnClient) handleTunPacket(pkt []byte) {

	if pkt == nil {
		pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("type", ERROR_IO).Set("error", "tun device close exception"))
		pc.Stop()
		return
	}
	version := pkt[0]
	version = version >> 4

	if version != VERSION_IP_V4 {
		return
	}

	var err error
	ipv4pkg := header.IPv4(pkt)
	direct := false
	self := false
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
				plog.Error("parser dns packet fail", err)
			} else {
				for i := 0; i < len(msg.Questions); i++ {
					name := msg.Questions[i].Name.String()
					name = name[:len(name)-1]
					if geoip.IsDirectDomainRecursive(name) {
						plog.Debugf("DNS CN DOMAIN %v", name)
						direct = true
					}
					if name == pc.host {
						self = true
					}
				}
			}
		} else {
			if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
				direct = true
			}
		}

	} else {
		plog.Infof("unknown packet ip type=%v,transport type=%v", ipv4pkg.Protocol(), ipv4pkg.TransportProtocol())
		return
	}

	if pc.mode {
		direct = false
	}

	if self {
		direct = true
	}

	if direct {
		pc.sendIPPacketToLocalForwarder(pkt)
	} else {
		pc.sendIPPacketToRemoteConn(pkt)
	}

}

func (pc *PoleVpnClient) sendIPPacketToLocalForwarder(pkt []byte) {
	if pc.forwarder != nil {
		pc.forwarder.Write(pkt)
	} else {
		plog.Error("local forwarder haven't set")
	}
}

func (pc *PoleVpnClient) sendIPPacketToRemoteConn(pkt []byte) {

	if pc.conn != nil {
		buf := make([]byte, POLE_PACKET_HEADER_LEN+len(pkt))
		copy(buf[POLE_PACKET_HEADER_LEN:], pkt)
		polepkt := PolePacket(buf)
		polepkt.SetCmd(CMD_C2S_IPDATA)
		polepkt.SetLen(uint16(len(buf)))
		pc.conn.Send(polepkt)
	} else {
		plog.Error("remote ws conn haven't set")
	}

}

func (pc *PoleVpnClient) AskAllocIPAddress() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_ALLOC_IPADDR)
	PolePacket(buf).SetLen(POLE_PACKET_HEADER_LEN)
	pc.conn.Send(buf)
}

func (pc *PoleVpnClient) handlerAllocAdressRespose(pkt PolePacket, conn Conn) {

	av, err := anyvalue.NewFromJson(pkt.Payload())

	if err != nil {
		plog.Error("alloc address av decode fail", err)
		pc.Stop()
		return
	}

	ip1 := av.Get("ip").AsStr()

	if ip1 == "" {
		plog.Error("alloc ip fail,stop client")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "alloc ip fail").Set("type", ERROR_ALLOC))
		}
		pc.Stop()
		return
	}

	pc.allocip = ip1

	if pc.handler != nil {
		pc.handler(CLIENT_EVENT_ADDRESS_ALLOCED, pc, av)
	}
}

func (pc *PoleVpnClient) handlerHeartBeatRespose(pkt PolePacket, conn Conn) {
	plog.Debug("received heartbeat")
	pc.lasttimeHeartbeat = time.Now()
}

func (pc *PoleVpnClient) handlerIPDataResponse(pkt PolePacket, conn Conn) {
	pc.tunio.Enqueue(pkt[POLE_PACKET_HEADER_LEN:])
}

func (pc *PoleVpnClient) handlerConnCloseEvent(pkt PolePacket, conn Conn) {
	plog.Info("client closed,start reconnect")
	pc.reconnect()
}

func (pc *PoleVpnClient) reconnect() {

	if pc.reconnecting == true {
		plog.Info("conn is reconnecting")
		return
	}

	pc.reconnecting = true
	pc.state = POLE_CLIENT_RECONNETING

	success := false
	for i := 0; i < RECONNECT_TIMES; i++ {

		if pc.state == POLE_CLIENT_CLOSED {
			break
		}

		plog.Info("reconnecting")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_RECONNECTING, pc, nil)
		}
		err := pc.conn.Connect(pc.endpoint, pc.user, pc.pwd, pc.allocip)

		if pc.state == POLE_CLIENT_CLOSED {
			break
		}

		if err != nil {
			if err == ErrNetwork {
				if i < 10 {
					time.Sleep(time.Second)
					plog.Error("retry 1 seconds later")
				} else {
					time.Sleep(time.Second * RECONNECT_INTERVAL)
					plog.Error("retry " + strconv.Itoa(RECONNECT_INTERVAL) + " seconds later")
				}
				continue
			} else if err == ErrIPNotExist {
				plog.Error("ip address expired,reconnect and request new")
				pc.allocip = ""
			} else {
				plog.Error("server refuse connect")
				break
			}

		} else {
			plog.Info("reconnect ok")
			if pc.allocip == "" {
				pc.AskAllocIPAddress()
			}
			pc.conn.StartProcess()
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
		plog.Error("reconnet failed,stop client")
		if pc.handler != nil {
			pc.handler(CLIENT_EVENT_ERROR, pc, anyvalue.New().Set("error", "reconnet failed").Set("type", ERROR_NETWORK))
		}
		pc.Stop()
	}
	pc.reconnecting = false
}

func (pc *PoleVpnClient) SendHeartBeat() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_HEART_BEAT)
	PolePacket(buf).SetLen(POLE_PACKET_HEADER_LEN)
	pc.conn.Send(buf)
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
			plog.Error("have not recevied heartbeat for", WEBSOCKET_NO_HEARTBEAT_TIMES, "times,close connection and reconnet")
			pc.conn.Close(true)
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
		plog.Error("client have been closed")
		return
	}

	pc.forwarder.Close()

	if pc.conn != nil {
		pc.conn.Close(false)
	}

	if pc.tunio != nil {
		pc.tunio.Close()
	}
	pc.state = POLE_CLIENT_CLOSED

	if pc.handler != nil {
		pc.handler(CLIENT_EVENT_STOPPED, pc, nil)
	}
	pc.wg.Done()
}
