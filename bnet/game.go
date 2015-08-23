package bnet

import (
	"github.com/HearthSim/hs-proto-go/bnet/game_utilities_service"
	"github.com/golang/protobuf/proto"
	"log"
)

// A GameServer accepts clients to create sessions.
type GameServer interface {
	// Connect connects the bnet session to the game session.  The game server
	// should set up the Client and ServerNotification channels on sess.
	Connect(sess *Session)
}

type GameUtilitiesServiceBinder struct{}

func (GameUtilitiesServiceBinder) Bind(sess *Session) Service {
	res := &GameUtilitiesService{}
	res.sess = sess
	return res
}

// The GameUtilities service arbitrates packets between the client and servers
// specific to individual games.
type GameUtilitiesService struct {
	sess *Session
}

func (s *GameUtilitiesService) Name() string {
	return "bnet.protocol.game_utilities.GameUtilities"
}

func (s *GameUtilitiesService) Methods() []string {
	return []string{
		"",
		"ProcessClientRequest",
		"PresenceChannelCreated",
		"GetPlayerVariables",
		"",
		"GetLoad",
		"ProcessServerRequest",
		"NotifyGameAccountOnline",
		"NotifyGameAccountOffline",
	}
}

func (s *GameUtilitiesService) Invoke(method int, body []byte) (resp []byte, err error) {
	switch method {
	case 1:
		return s.ProcessClientRequest(body)
	case 2:
		return []byte{}, s.PresenceChannelCreated(body)
	case 3:
		return s.GetPlayerVariables(body)
	case 5:
		return s.GetLoad(body)
	case 6:
		return s.ProcessServerRequest(body)
	case 7:
		return nil, s.NotifyGameAccountOnline(body)
	case 8:
		return nil, s.NotifyGameAccountOffline(body)
	default:
		log.Panicf("error: GameUtilitiesService.Invoke: unknown method %v", method)
		return
	}
}

func (s *GameUtilitiesService) ProcessClientRequest(body []byte) ([]byte, error) {
	req := game_utilities_service.ClientRequest{}
	err := proto.Unmarshal(body, &req)
	if err != nil {
		return nil, err
	}
	log.Printf("req = %s", req.String())
	token := s.sess.receivedToken
	s.sess.OnceNotified(NotifyClientResponse, func(n *Notification) {
		log.Printf("received client response notification")
		if len(n.Attributes) < 2 {
			// Client will error otherwise
			return
		}
		res := &game_utilities_service.ClientResponse{}
		res.Attribute = n.Attributes
		buf, err := proto.Marshal(res)
		if err != nil {
			log.Panicf("error: GameUtilitiesService: marshal: %v", err)
		}
		s.sess.Respond(token, buf)
	})
	s.sess.ServerNotifications <- &Notification{
		NotifyClientRequest,
		req.Attribute,
	}
	return nil, nil
}

func (s *GameUtilitiesService) PresenceChannelCreated(body []byte) error {
	return nyi
}

func (s *GameUtilitiesService) GetPlayerVariables(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameUtilitiesService) GetLoad(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameUtilitiesService) ProcessServerRequest(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameUtilitiesService) NotifyGameAccountOnline(body []byte) error {
	return nyi
}

func (s *GameUtilitiesService) NotifyGameAccountOffline(body []byte) error {
	return nyi
}
