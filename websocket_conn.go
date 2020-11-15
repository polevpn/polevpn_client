package main

import (
	"crypto/tls"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/polevpn/elog"
)

const (
	CH_WEBSOCKET_WRITE_SIZE = 2048
)

type WebSocketConn struct {
	conn    *websocket.Conn
	wch     chan []byte
	closed  bool
	handler map[uint16]func(PolePacket, *WebSocketConn)
	mutex   *sync.Mutex
}

func NewWebSocketConn() *WebSocketConn {
	return &WebSocketConn{
		conn:    nil,
		closed:  true,
		wch:     nil,
		handler: make(map[uint16]func(PolePacket, *WebSocketConn)),
		mutex:   &sync.Mutex{},
	}
}

func (wsc *WebSocketConn) Connect(endpoint string, user string, pwd string, sni string) error {

	localip, err := GetLocalIp()
	if err != nil {
		return err
	}

	tlsconfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	}

	d := websocket.Dialer{
		NetDialContext:  (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localip)}}).DialContext,
		TLSClientConfig: tlsconfig,
	}
	conn, _, err := d.Dial(endpoint+"?user="+url.QueryEscape(user)+"&pwd="+url.QueryEscape(pwd), nil)

	if err != nil {
		return err
	}

	wsc.mutex.Lock()
	defer wsc.mutex.Unlock()

	wsc.conn = conn
	wsc.wch = make(chan []byte, CH_WEBSOCKET_WRITE_SIZE)
	wsc.closed = false
	return nil
}

func (wsc *WebSocketConn) ReConnect(endpoint string, user string, pwd string, ip string, sni string) error {

	localip, err := GetLocalIp()
	if err != nil {
		return err
	}

	tlsconfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	}

	d := websocket.Dialer{
		NetDialContext:  (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localip)}}).DialContext,
		TLSClientConfig: tlsconfig,
	}

	conn, _, err := d.Dial(endpoint+"?user="+url.QueryEscape(user)+"&pwd="+url.QueryEscape(pwd)+"&ip="+ip, nil)

	if err != nil {
		return err
	}

	wsc.mutex.Lock()
	defer wsc.mutex.Unlock()

	wsc.conn = conn
	wsc.wch = make(chan []byte, CH_WEBSOCKET_WRITE_SIZE)
	wsc.closed = false
	return nil
}

func (wsc *WebSocketConn) Close(flag bool) error {
	wsc.mutex.Lock()
	defer wsc.mutex.Unlock()

	if wsc.closed == false {
		wsc.closed = true
		if wsc.wch != nil {
			wsc.wch <- nil
			close(wsc.wch)
		}
		err := wsc.conn.Close()
		if flag {
			pkt := make([]byte, POLE_PACKET_HEADER_LEN)
			PolePacket(pkt).SetCmd(CMD_CLIENT_CLOSED)
			go wsc.dispatch(pkt)
		}
		return err
	}
	return nil
}

func (wsc *WebSocketConn) String() string {
	return wsc.conn.LocalAddr().String() + "->" + wsc.conn.RemoteAddr().String()
}

func (wsc *WebSocketConn) IsClosed() bool {
	wsc.mutex.Lock()
	defer wsc.mutex.Unlock()

	return wsc.closed
}

func (wsc *WebSocketConn) SetHandler(cmd uint16, handler func(PolePacket, *WebSocketConn)) {
	wsc.handler[cmd] = handler
}

func (wsc *WebSocketConn) read() {
	defer func() {
		wsc.Close(true)
	}()

	defer PanicHandler()

	for {
		mtype, pkt, err := wsc.conn.ReadMessage()
		if err != nil {
			if err == io.EOF {
				elog.Info(wsc.String(), "conn closed")
			} else {
				elog.Error(wsc.String(), "conn read exception:", err)
			}
			return
		}
		if mtype != websocket.BinaryMessage {
			continue
		}

		wsc.dispatch(pkt)

	}

}

func (wsc *WebSocketConn) dispatch(pkt []byte) {
	ppkt := PolePacket(pkt)

	handler, ok := wsc.handler[ppkt.Cmd()]
	if ok {
		handler(pkt, wsc)
	} else {
		elog.Error("invalid pkt cmd=", ppkt.Cmd())
	}
}

func (wsc *WebSocketConn) write() {
	defer PanicHandler()

	for {
		select {
		case pkt, ok := <-wsc.wch:
			if !ok {
				elog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					elog.Info("exit write process")
					return
				}
				err := wsc.conn.WriteMessage(websocket.BinaryMessage, pkt)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						elog.Info(wsc.String(), "conn closed")
					} else {
						elog.Error(wsc.String(), "conn write exception:", err)
					}
					return
				}
			}
		}
	}
}

func (wsc *WebSocketConn) Send(pkt []byte) {
	if wsc.IsClosed() == true {
		elog.Debug("websocket connection is closed,can't send pkt")
		return
	}
	if wsc.wch != nil {
		wsc.wch <- pkt
	}
}

func (wsc *WebSocketConn) StartProcess() {
	go wsc.read()
	go wsc.write()
}
