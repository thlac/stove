package bnet

import (
	"fmt"
	"github.com/HearthSim/hs-proto-go/bnet/rpc"
	"github.com/golang/protobuf/proto"
	"log"
	"net"
	"time"
)

// Use nyi to error from unimplemented service methods.
var nyi = fmt.Errorf("nyi")

// A Service is a set of RPC methods bound to a particular Session.
type Service interface {
	// Name returns the fully qualified name of the service.
	Name() string

	// Methods returns the names of the service's invokable methods.
	Methods() []string

	// Invoke executes a method.
	Invoke(method int, body []byte) (resp []byte, err error)
}

// A ServiceBinder is used to create instances of a service bound to individual
// sessions.
type ServiceBinder interface {
	// Bind binds a service to a session.  Passing nil will give a default
	// instance which can be used to inspect the service and method names.
	Bind(sess *Session) Service
}

// ServiceHash returns a hash of the service's fully qualified name.
func ServiceHash(binder ServiceBinder) uint32 {
	s := binder.Bind(nil)
	return Hash(s.Name())
}

// A Server accepts client connections and processes each connection as a
// Session.
type Server struct {
	// Registered services are mapped by their service hash to a service binder.
	registeredServices map[uint32]ServiceBinder

	// Registered game servers are mapped by their product FourCCs.
	gameServers map[string]GameServer
}

func NewServer() *Server {
	s := &Server{}
	s.registeredServices = map[uint32]ServiceBinder{}
	s.gameServers = map[string]GameServer{}

	s.registerService(ConnectionServiceBinder{})
	// Server exports:
	s.registerService(AccountServiceBinder{})
	s.registerService(AuthServerServiceBinder{})
	s.registerService(ChannelInvitationServiceBinder{})
	s.registerService(FriendsServiceBinder{})
	s.registerService(GameUtilitiesServiceBinder{})
	s.registerService(GameMasterServiceBinder{})
	s.registerService(PresenceServiceBinder{})
	s.registerService(ResourcesServiceBinder{})
	// Client exports:
	s.registerService(AuthClientServiceBinder{})
	s.registerService(ChallengeNotifyServiceBinder{})
	s.registerService(NotificationListenerServiceBinder{})

	return s
}

func (s *Server) RegisterGameServer(fourcc string, serv GameServer) {
	s.gameServers[fourcc] = serv
}

func (s *Server) registerService(binder ServiceBinder) {
	s.registeredServices[ServiceHash(binder)] = binder
}

// ListenAndServe listens for incoming connections on the specified address
// indefinitely, handling each connection as a new Session.
func (s *Server) ListenAndServe(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handleClient(c)
	}
}

func (s *Server) ConnectGameServer(client *Session, program string) {
	if serv, ok := s.gameServers[program]; ok {
		serv.Connect(client)
	} else {
		log.Panicf("Server.ConnectGameServer: unregistered game: %s", program)
	}
}

func (s *Server) handleClient(c net.Conn) {
	c.SetDeadline(time.Time{})

	sess := NewSession(s, c)
	defer sess.DisconnectOnPanic()
	buf := make([]byte, 0x1000)
	for {
		_, err := sess.conn.Read(buf[:2])
		if err != nil {
			log.Panicf("error: Server.handleClient: length read: %v", err)
		}
		headerLen := int(buf[0])<<8 | int(buf[1])
		if headerLen > len(buf) {
			buf = append(buf, make([]byte, headerLen-len(buf))...)
		}
		_, err = sess.conn.Read(buf[:headerLen])
		if err != nil {
			log.Panicf("error: Server.handleClient: header read: %v", err)
		}
		header := rpc.Header{}
		err = proto.Unmarshal(buf[:headerLen], &header)
		if err != nil {
			log.Panicf("error: Server.handleClient: header decode: %v", err)
		}
		bodyLen := int(header.GetSize())
		var body []byte
		if bodyLen > 0 {
			if bodyLen > len(buf) {
				buf = append(buf, make([]byte, bodyLen-len(buf))...)
			}
			body = buf[:bodyLen]
			_, err = sess.conn.Read(body)
			if err != nil {
				log.Panicf("error: Server.handleClient: body read: %v", err)
			}
		}
		log.Printf("handling packet %s %x", header.String(), body)
		sess.HandlePacket(&header, body)
	}
}
