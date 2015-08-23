package bnet

import (
	"github.com/HearthSim/hs-proto-go/bnet/rpc"
	"github.com/golang/protobuf/proto"
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

const (
	StateDisconnected = iota
	StateConnecting
	StateConnected
	StateLoggingIn
	StateAuthenticationFailed
	StateReady
	StateCount
)

type Session struct {
	// ServerNotifications are notifications sent by the bnet server to the game
	// server.
	ServerNotifications chan<- *Notification
	// ClientNotifications are notifications sent by the game server to the bnet
	// server.
	ClientNotifications <-chan *Notification

	notificationHandlers map[string]chan NotifyHandler

	server *Server
	conn   net.Conn

	// Exports contain methods the client may invoke on the server; the client
	// refers to these as imports.
	exports []Service
	// Imports contain methods the server may invoke on the client; the client
	// refers to these as exports.
	imports []Service
	// Maps an imported service hash to an index into imports.
	importMap map[uint32]int

	// A request registers itself for a response by assigning to this map a
	// channel on which it will listen for the response.
	responses map[uint32]chan []byte
	// The token used for request/response pairs increments sequentially.
	lastToken uint32
	// This token is the most recently received token sent by the client.
	receivedToken uint32

	// This channel contains outgoing packets.
	packetQueue chan []byte

	// stateMutex protects Session.State
	stateMutex     sync.Mutex
	stateChange    *sync.Cond
	stateListeners map[int]int32
	// state is the current state of the session; it may be any of the State
	// consts defined above.
	state int

	startedPlaying time.Time
}

func NewSession(s *Server, c net.Conn) *Session {
	sess := &Session{}
	sess.server = s
	sess.conn = c
	sess.importMap = map[uint32]int{}
	sess.responses = map[uint32]chan []byte{}
	sess.packetQueue = make(chan []byte, 1)
	sess.stateChange = sync.NewCond(&sess.stateMutex)
	sess.stateListeners = map[int]int32{}
	sess.notificationHandlers = map[string]chan NotifyHandler{}
	for i := 0; i < StateCount; i++ {
		sess.stateListeners[i] = 0
	}
	sess.state = StateConnecting
	// The connection service export is implicity bound at index 0:
	sess.BindExport(0, Hash("bnet.protocol.connection.ConnectionService"))
	sess.BindImport(0, Hash("bnet.protocol.connection.ConnectionService"))
	go sess.pumpPacketQueue()
	return sess
}

func (s *Session) BindExport(index int, hash uint32) {
	var service Service = nil
	binder, ok := s.server.registeredServices[hash]
	if !ok {
		log.Printf("warn: Session.BindExport: unknown service: %d=%x", index, hash)
		// We still want to put a nil in the slot, so that we panic when the
		// service is invoked.
	} else {
		service = binder.Bind(s)
	}
	if index >= len(s.exports) {
		padLen := (1 + index) - len(s.exports)
		s.exports = append(s.exports, make([]Service, padLen)...)
	}
	s.exports[index] = service
}

func (s *Session) BindImport(index int, hash uint32) {
	var service Service = nil
	binder, ok := s.server.registeredServices[hash]
	if !ok {
		log.Printf("warn: Session.BindImport: unknown service: %d=%x", index, hash)
	} else {
		service = binder.Bind(s)
	}
	if index >= len(s.imports) {
		padLen := (1 + index) - len(s.imports)
		s.imports = append(s.imports, make([]Service, padLen)...)
	}
	s.imports[index] = service
	s.importMap[hash] = index
}

func (s *Session) ImportedService(name string) Service {
	for _, imp := range s.imports {
		if imp != nil && imp.Name() == name {
			return imp
		}
	}
	return nil
}

func (s *Session) QueuePacket(header *rpc.Header, buf []byte) error {
	packet, err := MakePacket(header, buf)
	if err != nil {
		return err
	}
	s.packetQueue <- packet
	return nil
}

// Goroutine to pump the outgoing packet queue
func (s *Session) pumpPacketQueue() {
	defer s.DisconnectOnPanic()
	quit := s.ChanForTransition(StateDisconnected)
	for {
		select {
		case packet := <-s.packetQueue:
			w, err := s.conn.Write(packet)
			if w != len(packet) {
				panic(err)
			}
			log.Printf("Wrote %d bytes", len(packet))
			if err != nil {
				log.Panicf("error: Session.WritePacketQueue failed: %v", err)
			}
		case <-quit:
			return
		}
	}
}

func (s *Session) DisconnectOnPanic() {
	if err := recover(); err != nil {
		log.Printf("session error: %v\n=== STACK TRACE ===\n%s",
			err, string(debug.Stack()))
		log.Println("closing session")
		s.Transition(StateDisconnected)
		s.conn.Close()
	}
}

func (s *Session) Disconnect() {
	s.conn.Close()
	s.Transition(StateDisconnected)
}

func (s *Session) MakeRequestHeader(service Service, methodId, size int) *rpc.Header {
	serviceId, ok := s.importMap[Hash(service.Name())]
	if !ok {
		log.Panicf("Client didn't export service %s", service.Name())
	}
	token := s.lastToken
	s.lastToken++
	return &rpc.Header{
		ServiceId: proto.Uint32(uint32(serviceId)),
		MethodId:  proto.Uint32(uint32(methodId)),
		Token:     proto.Uint32(token),
		Size:      proto.Uint32(uint32(size)),
	}
}

func (s *Session) HandlePacket(header *rpc.Header, body []byte) {
	if s.state == StateDisconnected {
		panic("cannot handle packets from disconnected clients")
	}
	serviceId := int(header.GetServiceId())
	methodId := int(header.GetMethodId())
	s.receivedToken = header.GetToken()

	if serviceId == 254 {
		s.HandleResponse(header.GetToken(), body)
	} else {
		resp := s.HandleRequest(serviceId, methodId, body)
		if resp != nil {
			respHead := &rpc.Header{
				ServiceId: proto.Uint32(254),
				Token:     header.Token,
				Size:      proto.Uint32(uint32(len(resp))),
			}
			err := s.QueuePacket(respHead, resp)
			if err != nil {
				log.Panicf("error: Session.HandlePacket: respond: %v", err)
			}
		}
	}
}

func (s *Session) Respond(token uint32, body []byte) {
	err := s.QueuePacket(&rpc.Header{
		ServiceId: proto.Uint32(254),
		Token:     proto.Uint32(token),
		Size:      proto.Uint32(uint32(len(body))),
	}, body)
	if err != nil {
		log.Panicf("error: Session.Respond: %v", err)
	}
}

func (s *Session) HandleResponse(token uint32, body []byte) {
	if ch, ok := s.responses[token]; ok {
		// Note: don't use unbuffered channels for response channels, please...
		ch <- body
	} else {
		log.Printf(" warn: Session.HandleResponse: token not found: %v", token)
	}
}

func (s *Session) HandleRequest(serviceId, methodId int, body []byte) (resp []byte) {
	var service Service
	if serviceId < len(s.exports) {
		service = s.exports[serviceId]
	}
	if service == nil {
		log.Panicf("error: Session.HandleRequest: Unknown serviceId %v", serviceId)
	}
	serviceName := service.Name()
	methodNames := service.Methods()
	methodName := "(unknown)"
	if len(methodNames) > methodId {
		methodName = methodNames[methodId]
	}
	log.Printf("Session.HandleRequest: invoking %s.%s", serviceName, methodName)
	resp, err := service.Invoke(methodId, body)
	if err != nil {
		log.Panicf("error: Session.HandleRequest: Invoke: %v", err)
	}
	return resp
}

// Transition updates the session's state with the value provided, and notifies
// any listeners of that state update.
func (s *Session) Transition(state int) {
	s.stateMutex.Lock()
	s.state = state
	s.stateMutex.Unlock()
	s.stateChange.Broadcast()
	// If every broadcast hasn't triggered yet, we should wait:
	for s.stateListeners[state] != 0 {
		time.Sleep(time.Millisecond)
	}
}

// WaitForTransition blocks until the session state matches the value provided.
func (s *Session) WaitForTransition(state int) {
	s.stateMutex.Lock()
	s.stateListeners[state] += 1
	for s.state != state {
		s.stateChange.Wait()
	}
	s.stateListeners[state] -= 1
	s.stateMutex.Unlock()
}

// ChanForTransition makes a channel that waits for the specified state.
func (s *Session) ChanForTransition(state int) chan struct{} {
	res := make(chan struct{}, 1)
	go func() {
		s.WaitForTransition(state)
		res <- struct{}{}
	}()
	return res
}
