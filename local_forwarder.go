package main

import (
	"errors"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/polevpn/elog"
	"github.com/polevpn/netstack/tcpip"
	"github.com/polevpn/netstack/tcpip/buffer"
	"github.com/polevpn/netstack/tcpip/link/channel"
	"github.com/polevpn/netstack/tcpip/network/arp"
	"github.com/polevpn/netstack/tcpip/network/ipv4"
	"github.com/polevpn/netstack/tcpip/stack"
	"github.com/polevpn/netstack/tcpip/transport/tcp"
	"github.com/polevpn/netstack/tcpip/transport/udp"
	"github.com/polevpn/netstack/waiter"
)

const (
	FORWARD_UDP_TIMEOUT      = 10
	FORWARD_CH_WRITE_SIZE    = 256
	UDP_MAX_BUFFER_SIZE      = 8192
	UDP_CONNECTION_IDLE_TIME = 2
)

type LocalForwarder struct {
	s     *stack.Stack
	ep    *channel.Endpoint
	wq    *waiter.Queue
	tunio *TunIO
}

func NewLocalForwarder() (*LocalForwarder, error) {

	forwarder := &LocalForwarder{}

	maddr, err := net.ParseMAC("01:01:01:01:01:01")
	if err != nil {
		return nil, err
	}

	// Create the stack with ip and tcp protocols, then add a tun-based
	// NIC and address.
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocol{ipv4.NewProtocol(), arp.NewProtocol()},
		TransportProtocols: []stack.TransportProtocol{tcp.NewProtocol(), udp.NewProtocol()},
	})

	ep := channel.New(FORWARD_CH_WRITE_SIZE, 1500, tcpip.LinkAddress(maddr))

	if err := s.CreateNIC(1, ep); err != nil {
		return nil, errors.New(err.String())
	}

	subnet1, err := tcpip.NewSubnet(tcpip.Address(net.IPv4(0, 0, 0, 0).To4()), tcpip.AddressMask(net.IPv4Mask(0, 0, 0, 0)))
	if err != nil {
		return nil, err
	}

	if err := s.AddAddressRange(1, ipv4.ProtocolNumber, subnet1); err != nil {
		return nil, errors.New(err.String())
	}

	if err := s.AddAddress(1, arp.ProtocolNumber, arp.ProtocolAddress); err != nil {
		return nil, errors.New(err.String())
	}

	subnet, err := tcpip.NewSubnet(tcpip.Address(net.IPv4(0, 0, 0, 0).To4()), tcpip.AddressMask(net.IPv4Mask(0, 0, 0, 0)))
	if err != nil {
		return nil, err
	}
	// Add default route.
	s.SetRouteTable([]tcpip.Route{
		{
			Destination: subnet,
			NIC:         1,
		},
	})

	uf := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		go forwarder.forwardUDP(r)
	})

	s.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)

	tf := tcp.NewForwarder(s, 0, 10, func(r *tcp.ForwarderRequest) {
		go forwarder.forwardTCP(r)
	})

	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)

	forwarder.s = s
	forwarder.ep = ep
	forwarder.wq = &waiter.Queue{}
	return forwarder, nil

}

func (lf *LocalForwarder) SetTunIO(tunio *TunIO) {
	lf.tunio = tunio
}

func (lf *LocalForwarder) Write(pkg []byte) {
	pkgBuffer := tcpip.PacketBuffer{Data: buffer.NewViewFromBytes(pkg).ToVectorisedView()}
	lf.ep.InjectInbound(ipv4.ProtocolNumber, pkgBuffer)
}

func (lf *LocalForwarder) read() {
	for {
		pkgInfo, ok := <-lf.ep.C
		if !ok {
			elog.Error("get pkt from link channel fail,maybe channel closed")
			return
		}
		view := buffer.NewVectorisedView(1, []buffer.View{pkgInfo.Pkt.Header.View()})
		view.Append(pkgInfo.Pkt.Data)
		if lf.tunio != nil {
			lf.tunio.Enqueue(view.ToView())
		}
	}
}

func (lf *LocalForwarder) StartProcess() {
	go lf.read()
}

func (lf *LocalForwarder) Close() {
	lf.s.Close()
	close(lf.ep.C)
}

func (lf *LocalForwarder) forwardTCP(r *tcp.ForwarderRequest) {

	ep, err := r.CreateEndpoint(lf.wq)
	if err != nil {
		elog.Error("create tcp endpint error", err)
		return
	}

	localip, err1 := GetLocalIp()
	if err1 != nil {
		elog.Error("get local ip fail", err1)
		return
	}

	addr, _ := ep.GetLocalAddress()
	laddr, _ := net.ResolveTCPAddr("tcp4", localip+":0")
	raddr, _ := net.ResolveTCPAddr("tcp4", addr.Addr.String()+":"+strconv.Itoa(int(addr.Port)))

	conn, err1 := net.DialTCP("tcp4", laddr, raddr)
	if err1 != nil {
		log.Println("conn dial error ", err1)
		ep.Close()
		return
	}
	go lf.tcpRead(lf.wq, ep, conn)
	go lf.tcpWrite(lf.wq, ep, conn)
}

func (lf *LocalForwarder) udpRead(ep tcpip.Endpoint, conn *net.UDPConn, timer *time.Ticker) {

	defer ep.Close()
	defer conn.Close()

	waitEntry, notifyCh := waiter.NewChannelEntry(nil)

	lf.wq.EventRegister(&waitEntry, waiter.EventIn)
	defer lf.wq.EventUnregister(&waitEntry)

	lastTime := time.Now()

	for {
		var addr tcpip.FullAddress
		v, _, err := ep.Read(&addr)
		if err != nil {
			if err == tcpip.ErrWouldBlock {

				select {
				case <-notifyCh:
					continue
				case <-timer.C:
					if time.Now().Sub(lastTime) > time.Minute*UDP_CONNECTION_IDLE_TIME {
						elog.Info("udp connection expired,close it")
						timer.Stop()
						return
					} else {
						continue
					}
				}

			}
			elog.Error("read from udp endpoint fail", err)
			return
		}

		_, err1 := conn.Write(v)

		if err1 != nil {
			elog.Error("udp conn writeto error ", err1)
			return
		}
		lastTime = time.Now()
	}
}

func (lf *LocalForwarder) udpWrite(ep tcpip.Endpoint, conn *net.UDPConn, addr *tcpip.FullAddress) {

	defer ep.Close()
	defer conn.Close()

	for {
		var udppkg []byte = make([]byte, UDP_MAX_BUFFER_SIZE)
		n, err1 := conn.Read(udppkg)

		if err1 != nil {
			elog.Error("udp conn readfrom error ", err1)
			return
		}
		udppkg1 := udppkg[:n]
		_, _, err := ep.Write(tcpip.SlicePayload(udppkg1), tcpip.WriteOptions{To: addr})
		if err != nil {
			elog.Error("udp ep write data fail ", err)
			return
		}
	}
}

func (lf *LocalForwarder) forwardUDP(r *udp.ForwarderRequest) {

	ep, err := r.CreateEndpoint(lf.wq)
	if err != nil {
		elog.Error("create endpint error", err)
		return
	}

	localip, err1 := GetLocalIp()
	if err1 != nil {
		elog.Error("get local ip fail", err1)
		return
	}

	laddr, _ := net.ResolveUDPAddr("udp4", localip+":0")
	raddr, _ := net.ResolveUDPAddr("udp4", r.ID().LocalAddress.To4().String()+":"+strconv.Itoa(int(r.ID().LocalPort)))

	conn, err1 := net.DialUDP("udp4", laddr, raddr)
	if err1 != nil {
		elog.Error("udp conn dial error ", err1)
		ep.Close()
		return
	}

	timer := time.NewTicker(time.Minute)
	addr := &tcpip.FullAddress{Addr: r.ID().RemoteAddress, Port: r.ID().RemotePort}

	go lf.udpRead(ep, conn, timer)
	go lf.udpWrite(ep, conn, addr)
}

func (lf *LocalForwarder) tcpRead(wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer ep.Close()
	defer conn.Close()
	// Create wait queue entry that notifies a channel.
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)

	wq.EventRegister(&waitEntry, waiter.EventIn)
	defer wq.EventUnregister(&waitEntry)

	for {
		v, _, err := ep.Read(nil)
		if err != nil {
			if err == tcpip.ErrWouldBlock {
				<-notifyCh
				continue
			}
			elog.Error("endpoint read fail", err)
			return
		}
		_, err1 := conn.Write(v)
		if err1 != nil {
			elog.Error("conn write error", err1)
			return
		}
	}
}

func (lf *LocalForwarder) tcpWrite(wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer ep.Close()
	defer conn.Close()
	for {
		var buf []byte = make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			elog.Error("conn read error", err)
			break
		}

		ep.Write(tcpip.SlicePayload(buf[:n]), tcpip.WriteOptions{})
	}
}
