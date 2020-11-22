package core

import (
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

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
	TCP_MAX_CONNECTION_SIZE  = 1024
	FORWARD_CH_WRITE_SIZE    = 2048
	UDP_MAX_BUFFER_SIZE      = 8192
	TCP_MAX_BUFFER_SIZE      = 8192
	UDP_READ_BUFFER_SIZE     = 131072
	UDP_WRITE_BUFFER_SIZE    = 65536
	TCP_READ_BUFFER_SIZE     = 131072
	TCP_WRITE_BUFFER_SIZE    = 65536
	UDP_CONNECTION_IDLE_TIME = 1
	CH_WRITE_SIZE            = 1024
	TCP_CONNECT_TIMEOUT      = 10
)

type LocalForwarder struct {
	s       *stack.Stack
	ep      *channel.Endpoint
	wq      *waiter.Queue
	closed  bool
	handler func([]byte)
	localip string
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

	tf := tcp.NewForwarder(s, 0, TCP_MAX_CONNECTION_SIZE, func(r *tcp.ForwarderRequest) {
		go forwarder.forwardTCP(r)
	})

	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)
	forwarder.closed = false
	forwarder.s = s
	forwarder.ep = ep
	forwarder.wq = &waiter.Queue{}
	return forwarder, nil

}

func (lf *LocalForwarder) SetPacketHandler(handler func([]byte)) {
	lf.handler = handler
}

func (lf *LocalForwarder) SetLocalIP(ip string) {
	lf.localip = ip
}

func (lf *LocalForwarder) Write(pkg []byte) {
	if lf.closed {
		return
	}
	pkgBuffer := tcpip.PacketBuffer{Data: buffer.NewViewFromBytes(pkg).ToVectorisedView()}
	lf.ep.InjectInbound(ipv4.ProtocolNumber, pkgBuffer)
}

func (lf *LocalForwarder) read() {
	for {
		pkgInfo, err := lf.ep.Read()
		if err != nil {
			plog.Info(err)
			return
		}
		view := buffer.NewVectorisedView(1, []buffer.View{pkgInfo.Pkt.Header.View()})
		view.Append(pkgInfo.Pkt.Data)
		if lf.handler != nil {
			lf.handler(view.ToView())
		}
	}
}

func (lf *LocalForwarder) StartProcess() {
	go lf.read()
}

func (lf *LocalForwarder) Close() {
	defer PanicHandler()

	if lf.closed {
		return
	}
	lf.closed = true

	lf.wq.Notify(waiter.EventIn)
	lf.s.Close()
	time.Sleep(time.Millisecond * 100)
	lf.ep.Close()
}

func (lf *LocalForwarder) forwardTCP(r *tcp.ForwarderRequest) {

	wq := &waiter.Queue{}
	ep, err := r.CreateEndpoint(wq)
	if err != nil {
		plog.Error("create tcp endpint error", err)
		r.Complete(true)
		return
	}

	plog.Debug(r.ID(), "tcp connect")

	localip := lf.localip
	var err1 error
	if localip == "" {
		localip, err1 = GetLocalIp()
		if err1 != nil {
			plog.Error("get local ip fail", err1)
			r.Complete(true)
			ep.Close()
			return
		}
	}

	addr, _ := ep.GetLocalAddress()
	laddr, _ := net.ResolveTCPAddr("tcp4", localip+":0")
	raddr := addr.Addr.String() + ":" + strconv.Itoa(int(addr.Port))
	d := net.Dialer{Timeout: time.Second * TCP_CONNECT_TIMEOUT, LocalAddr: laddr}
	conn, err1 := d.Dial("tcp4", raddr)
	if err1 != nil {
		plog.Println("conn dial error ", err1)
		r.Complete(true)
		ep.Close()
		return
	}
	tcpconn := conn.(*net.TCPConn)
	tcpconn.SetNoDelay(true)
	tcpconn.SetKeepAlive(true)
	tcpconn.SetWriteBuffer(TCP_WRITE_BUFFER_SIZE)
	tcpconn.SetReadBuffer(TCP_READ_BUFFER_SIZE)
	tcpconn.SetKeepAlivePeriod(time.Second * 15)

	go lf.tcpRead(r, wq, ep, conn)
	go lf.tcpWrite(r, wq, ep, conn)
}

func (lf *LocalForwarder) udpRead(r *udp.ForwarderRequest, ep tcpip.Endpoint, wq *waiter.Queue, conn *net.UDPConn, timer *time.Ticker) {

	defer func() {
		plog.Debug(r.ID(), "udp closed")
		ep.Close()
		conn.Close()
	}()

	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	wq.EventRegister(&waitEntry, waiter.EventIn)
	defer wq.EventUnregister(&waitEntry)

	gwaitEntry, gnotifyCh := waiter.NewChannelEntry(nil)

	lf.wq.EventRegister(&gwaitEntry, waiter.EventIn)
	defer lf.wq.EventUnregister(&gwaitEntry)

	wch := make(chan []byte, CH_WRITE_SIZE)

	defer close(wch)

	writer := func() {
		for {
			pkt, ok := <-wch
			if !ok {
				plog.Debug("udp wch closed,exit write process")
				return
			} else {
				_, err1 := conn.Write(pkt)
				if err1 != nil {
					plog.Error("conn write error", err1)
					return
				}
			}
		}
	}

	go writer()

	lastTime := time.Now()

	for {
		var addr tcpip.FullAddress
		v, _, err := ep.Read(&addr)
		if err != nil {
			if err == tcpip.ErrWouldBlock {

				select {
				case <-notifyCh:
					continue
				case <-gnotifyCh:
					return
				case <-timer.C:
					if time.Now().Sub(lastTime) > time.Minute*UDP_CONNECTION_IDLE_TIME {
						plog.Debug("udp connection expired,close it")
						timer.Stop()
						return
					} else {
						continue
					}
				}
			} else if err != tcpip.ErrClosedForReceive && err != tcpip.ErrClosedForSend {
				plog.Error("read from udp endpoint fail", err)
			}
			return
		}

		wch <- v
		lastTime = time.Now()
	}
}

func (lf *LocalForwarder) udpWrite(r *udp.ForwarderRequest, ep tcpip.Endpoint, wq *waiter.Queue, conn *net.UDPConn, addr *tcpip.FullAddress) {

	defer func() {
		ep.Close()
		conn.Close()
	}()

	for {
		var udppkg []byte = make([]byte, UDP_MAX_BUFFER_SIZE)
		n, err1 := conn.Read(udppkg)

		if err1 != nil {
			if err1 != io.EOF && strings.Index(err1.Error(), "use of closed network connection") < 0 {
				plog.Error("udp conn readfrom error ", err1)
			}
			return
		}
		udppkg1 := udppkg[:n]
		_, _, err := ep.Write(tcpip.SlicePayload(udppkg1), tcpip.WriteOptions{To: addr})
		if err != nil {
			plog.Error("udp ep write data fail ", err)
			return
		}
	}
}

func (lf *LocalForwarder) forwardUDP(r *udp.ForwarderRequest) {
	wq := &waiter.Queue{}
	ep, err := r.CreateEndpoint(wq)
	if err != nil {
		plog.Error("create endpint error", err)
		return
	}

	plog.Debug(r.ID(), "udp connect")

	localip := lf.localip
	var err1 error
	if localip == "" {
		localip, err1 = GetLocalIp()
		if err1 != nil {
			plog.Error("get local ip fail", err1)
			ep.Close()
			return
		}
	}

	laddr, _ := net.ResolveUDPAddr("udp4", localip+":0")
	raddr, _ := net.ResolveUDPAddr("udp4", r.ID().LocalAddress.To4().String()+":"+strconv.Itoa(int(r.ID().LocalPort)))

	conn, err1 := net.DialUDP("udp4", laddr, raddr)
	if err1 != nil {
		plog.Error("udp conn dial error ", err1)
		ep.Close()
		return
	}

	conn.SetReadBuffer(UDP_READ_BUFFER_SIZE)
	conn.SetWriteBuffer(UDP_WRITE_BUFFER_SIZE)

	timer := time.NewTicker(time.Minute)
	addr := &tcpip.FullAddress{Addr: r.ID().RemoteAddress, Port: r.ID().RemotePort}

	go lf.udpRead(r, ep, wq, conn, timer)
	go lf.udpWrite(r, ep, wq, conn, addr)
}

func (lf *LocalForwarder) tcpRead(r *tcp.ForwarderRequest, wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer func() {
		plog.Debug(r.ID(), "tcp closed")
		r.Complete(true)
		ep.Close()
		conn.Close()
	}()

	// Create wait queue entry that notifies a channel.
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)

	wq.EventRegister(&waitEntry, waiter.EventIn)
	defer wq.EventUnregister(&waitEntry)

	// Create wait queue entry that notifies a channel.
	gwaitEntry, gnotifyCh := waiter.NewChannelEntry(nil)

	lf.wq.EventRegister(&gwaitEntry, waiter.EventIn)
	defer lf.wq.EventUnregister(&gwaitEntry)

	wch := make(chan []byte, CH_WRITE_SIZE)

	defer close(wch)

	writer := func() {
		for {
			pkt, ok := <-wch
			if !ok {
				plog.Debug("wch closed,exit write process")
				return
			} else {
				_, err1 := conn.Write(pkt)
				if err1 != nil {
					plog.Error("conn write error", err1)
					return
				}
			}
		}
	}

	go writer()

	for {
		v, _, err := ep.Read(nil)
		if err != nil {

			if err == tcpip.ErrWouldBlock {
				select {
				case <-notifyCh:
					continue
				case <-gnotifyCh:
					return
				}

			} else if err != tcpip.ErrClosedForReceive && err != tcpip.ErrClosedForSend {
				plog.Error("endpoint read fail", err)
			}
			return
		}
		wch <- v
	}
}

func typeof(v interface{}) {
	fmt.Println(reflect.TypeOf(v))
}

func (lf *LocalForwarder) tcpWrite(r *tcp.ForwarderRequest, wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer func() {
		ep.Close()
		conn.Close()
	}()

	for {
		var buf []byte = make([]byte, TCP_MAX_BUFFER_SIZE)
		n, err := conn.Read(buf)
		if err != nil {
			if err != io.EOF && strings.Index(err.Error(), "use of closed network connection") < 0 {
				plog.Error("conn read error", err)
			}
			break
		}

		ep.Write(tcpip.SlicePayload(buf[:n]), tcpip.WriteOptions{})
	}
}
