package core

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	CH_WEBSOCKET_WRITE_SIZE     = 2048
	WEBSOCKET_HANDSHAKE_TIMEOUT = 5
)

var ErrIPNotExist = errors.New("reconnect ip is not exist")
var ErrLoginVerify = errors.New("login verify fail")
var ErrConnectUnknown = errors.New("server unknown error")
var ErrNetwork = errors.New("network error")

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

func (wsc *WebSocketConn) Connect(endpoint string, user string, pwd string, ip string, sni string) error {

	localip, err := GetLocalIp()
	if err != nil {
		return err
	}

	tlsconfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	}

	d := websocket.Dialer{
		NetDialContext:   (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localip)}}).DialContext,
		TLSClientConfig:  tlsconfig,
		HandshakeTimeout: time.Second * WEBSOCKET_HANDSHAKE_TIMEOUT,
	}

	conn, resp, err := d.Dial(endpoint+"?user="+url.QueryEscape(user)+"&pwd="+url.QueryEscape(pwd)+"&ip="+ip, nil)

	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusBadRequest {
				return ErrIPNotExist
			} else if resp.StatusCode == http.StatusForbidden {
				return ErrLoginVerify
			} else {
				return ErrConnectUnknown
			}
		}
		plog.Error("websocket connect fail,", err)
		return ErrNetwork
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
			if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
				plog.Info(wsc.String(), "conn closed")
			} else {
				plog.Error(wsc.String(), "conn read exception:", err)
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
		plog.Error("invalid pkt cmd=", ppkt.Cmd())
	}
}

func (wsc *WebSocketConn) write() {
	defer PanicHandler()

	for {
		select {
		case pkt, ok := <-wsc.wch:
			if !ok {
				plog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					plog.Info("exit write process")
					return
				}
				err := wsc.conn.WriteMessage(websocket.BinaryMessage, pkt)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						plog.Info(wsc.String(), "conn closed")
					} else {
						plog.Error(wsc.String(), "conn write exception:", err)
					}
					return
				}
			}
		}
	}
}

func (wsc *WebSocketConn) Send(pkt []byte) {
	if wsc.IsClosed() == true {
		plog.Debug("websocket connection is closed,can't send pkt")
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