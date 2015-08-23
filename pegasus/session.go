package pegasus

import (
	"errors"
	"github.com/HearthSim/hs-proto-go/bnet/attribute"
	"github.com/HearthSim/stove/bnet"
	"github.com/golang/protobuf/proto"
	"log"
)

type Session struct {
	server   *Server
	host     *bnet.Session
	handlers map[PacketID]UtilHandler

	// hostNotifications are notifications sent by bnet to pegasus
	hostNotifications <-chan *bnet.Notification
	// gameNotifications are notifications sent by pegasus to bnet
	gameNotifications chan<- *bnet.Notification

	Account
	Draft
	Store
	Subscription
	Version
}

func BindSession(s *Server, hostSess *bnet.Session) {
	sess := &Session{}
	sess.server = s
	sess.host = hostSess
	sess.handlers = map[PacketID]UtilHandler{}

	sess.Account.Init(sess)
	sess.Draft.Init(sess)
	sess.Store.Init(sess)
	sess.Subscription.Init(sess)
	sess.Version.Init(sess)

	notifyTx := make(chan *bnet.Notification, 1)
	notifyRx := make(chan *bnet.Notification, 1)
	sess.hostNotifications = notifyRx
	sess.host.ServerNotifications = notifyRx
	sess.gameNotifications = notifyTx
	sess.host.ClientNotifications = notifyTx
	go sess.HandleNotifications()
}

func (s *Session) HandleNotifications() {
	quit := s.host.ChanForTransition(bnet.StateDisconnected)
	defer s.host.DisconnectOnPanic()
	for {
		select {
		case notify := <-s.hostNotifications:
			s.handleNotification(notify)
		case <-quit:
			return
		}
	}
}

func (s *Session) handleNotification(n *bnet.Notification) {
	switch n.Type {
	case bnet.NotifyClientRequest:
		s.HandleUtilRequest(n.Attributes)
	case bnet.NotifyFindGameRequest:
		s.HandleFindGame(n.Map())
	default:
		log.Panicf("unhandled notification type: %s", n.Type)
	}
}

var nyi = errors.New("not yet implemented")

// HandleUtilRequest processes an encoded client request from GameUtilities,
// possibly returning an encoded response.
func (s *Session) HandleUtilRequest(req []*attribute.Attribute) {
	var packetId, systemId int32
	// System 1 involves payment.  System 0 is everything else.
	route := uint64(0)
	var data []byte
	for _, attr := range req {
		key := attr.GetName()
		val := attr.GetValue()
		switch key {
		case "p":
			blob := val.GetBlobValue()
			if len(blob) < 2 {
				log.Panicf("bad util packet: %v", req)
			}
			packetId = int32(int(blob[0]) | int(blob[1])<<8)
			data = blob[2:]
		case "v":
			if val.IntValue != nil {
				intVal := int(val.GetIntValue())
				systemId = int32(intVal % 10)
			} else if val.StringValue != nil {
				strVal := val.GetStringValue()
				sysStr := strVal[len(strVal)-1:]
				switch sysStr {
				case "b": // BattlePay
					systemId = 1
				case "c": // ConnectAPI
					systemId = 0
				default:
					log.Panicf("bad util packet: %v", req)
				}
			} else {
				log.Panicf("bad util packet: %v", req)
			}
		case "r":
			route = val.GetUintValue()
		default:
			log.Printf("HandleUtilRequest: unknown key: %s = %s", key, val.String())
		}
	}
	if route != s.route {
		log.Printf("HandleUtilRequest: bad route")
	}
	attr := s.handleUtilRequest(systemId, packetId, data)
	// We still send even if attr is empty, because every ClientRequest must
	// have a ClientResponse.
	s.gameNotifications <- &bnet.Notification{bnet.NotifyClientResponse, attr}
}

func (s *Session) handleUtilRequest(systemId, packetId int32, req []byte) []*attribute.Attribute {
	id := PacketID{packetId, systemId}
	if handler, ok := s.handlers[id]; ok {
		pack := handler(s, req)
		if pack == nil {
			return nil
		}
		attr := make([]*attribute.Attribute, 2)
		attr[0] = &attribute.Attribute{
			Name: proto.String("t"),
			Value: &attribute.Variant{
				IntValue: proto.Int64(int64(pack.ID)),
			},
		}
		attr[1] = &attribute.Attribute{
			Name: proto.String("p"),
			Value: &attribute.Variant{
				BlobValue: pack.Body,
			},
		}
		return attr
	}
	log.Panicf("handler does not exist for util packet %d:%d", systemId, packetId)
	return nil
}

func (s *Session) RegisterPacket(packetId interface{}, handler UtilHandler) {
	id := packetIDFromProto(packetId)
	s.registerUtilHandler(id.System, id.ID, handler)
}

func (s *Session) UnregisterPacket(packetId interface{}, handler UtilHandler) {
	id := packetIDFromProto(packetId)
	s.unregisterUtilHandler(id.System, id.ID, handler)
}

func (s *Session) registerUtilHandler(systemId, packetId int32, handler UtilHandler) {
	id := PacketID{packetId, systemId}
	if _, ok := s.handlers[id]; !ok {
		s.handlers[id] = handler
	} else {
		log.Panicf("cannot overwrite existing handler for util packet %d:%d", systemId, packetId)
	}
}

func (s *Session) unregisterUtilHandler(systemId, packetId int32, handler UtilHandler) {
	id := PacketID{packetId, systemId}
	if _, ok := s.handlers[id]; ok {
		delete(s.handlers, id)
	} else {
		log.Panicf("unregister called for non-existent handler for util packet %d:%d", systemId, packetId)
	}
}
