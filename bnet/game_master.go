package bnet

import (
	"fmt"
	"github.com/HearthSim/hs-proto-go/bnet/game_master_service"
	"github.com/HearthSim/hs-proto-go/bnet/game_master_types"
	"github.com/golang/protobuf/proto"
	"log"
)

type GameMasterServiceBinder struct{}

func (GameMasterServiceBinder) Bind(sess *Session) Service {
	res := &GameMasterService{}
	res.sess = sess
	return res
}

// The GameUtilities service arbitrates packets between the client and servers
// specific to individual games.
type GameMasterService struct {
	sess *Session
}

func (s *GameMasterService) Name() string {
	return "bnet.protocol.game_master.GameMaster"
}

func (s *GameMasterService) Methods() []string {
	return []string{
		"",
		"JoinGame",
		"ListFactories",
		"FindGame",
		"CancelGameEntry",
		"GameEnded",
		"PlayerLeft",
		"RegisterServer",
		"UnregisterServer",
		"RegisterUtilities",
		"UnregisterUtilities",
		"Subscribe",
		"Unsubscribe",
		"ChangeGame",
		"GetFactoryInfo",
		"GetGameStats",
	}
}

func (s *GameMasterService) Invoke(method int, body []byte) (resp []byte, err error) {
	switch method {
	case 1:
		return s.JoinGame(body)
	case 2:
		return s.ListFactories(body)
	case 3:
		return s.FindGame(body)
	case 4:
		return []byte{}, s.CancelGameEntry(body)
	case 5:
		return nil, s.GameEnded(body)
	case 6:
		return []byte{}, s.PlayerLeft(body)
	case 7:
		return []byte{}, s.RegisterServer(body)
	case 8:
		return nil, s.UnregisterServer(body)
	case 9:
		return []byte{}, s.RegisterUtilities(body)
	case 10:
		return nil, s.UnregisterUtilities(body)
	case 11:
		return s.Subscribe(body)
	case 12:
		return nil, s.Unsubscribe(body)
	case 13:
		return []byte{}, s.ChangeGame(body)
	case 14:
		return s.GetFactoryInfo(body)
	case 15:
		return s.GetGameStats(body)
	default:
		log.Panicf("error: GameMasterService.Invoke: unknown method %v", method)
		return
	}
}

func (s *GameMasterService) JoinGame(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameMasterService) ListFactories(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameMasterService) FindGame(body []byte) ([]byte, error) {
	req := &game_master_service.FindGameRequest{}
	proto.Unmarshal(body, req)
	token := s.sess.receivedToken
	fmt.Println(req.String())
	advNotification := false
	if req.AdvancedNotification != nil {
		advNotification = *req.AdvancedNotification
	}
	player := req.Player[0]
	notify := NewNotification(NotifyFindGameRequest, map[string]interface{}{
		"advanced_notification": advNotification,
	})
	// TODO: care about game_properties and other stuff
	notify.Attributes = append(notify.Attributes, player.Attribute...)
	s.sess.OnceNotified(NotifyFindGameResponse, func(n *Notification) {
		m := n.Map()
		res := &game_master_service.FindGameResponse{}
		res.Queued = proto.Bool(m["queued"].(bool))
		res.RequestId = proto.Uint64(m["requestId"].(uint64))
		res.FactoryId = proto.Uint64(0)
		buf, err := proto.Marshal(res)
		if err != nil {
			panic(err)
		}
		s.sess.Respond(token, buf)
	})
	s.sess.ServerNotifications <- notify
	return nil, nil
}

func (s *GameMasterService) CancelGameEntry(body []byte) error {
	req := &game_master_types.CancelGameEntryRequest{}
	proto.Unmarshal(body, req)
	fmt.Printf("req = %s\n", req.String())
	return nil
}

func (s *GameMasterService) GameEnded(body []byte) error {
	return nyi
}

func (s *GameMasterService) PlayerLeft(body []byte) error {
	return nyi
}

func (s *GameMasterService) RegisterServer(body []byte) error {
	return nyi
}

func (s *GameMasterService) UnregisterServer(body []byte) error {
	return nyi
}

func (s *GameMasterService) RegisterUtilities(body []byte) error {
	return nyi
}

func (s *GameMasterService) UnregisterUtilities(body []byte) error {
	return nyi
}

func (s *GameMasterService) Subscribe(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameMasterService) Unsubscribe(body []byte) error {
	return nyi
}

func (s *GameMasterService) ChangeGame(body []byte) error {
	return nyi
}

func (s *GameMasterService) GetFactoryInfo(body []byte) ([]byte, error) {
	return nil, nyi
}

func (s *GameMasterService) GetGameStats(body []byte) ([]byte, error) {
	return nil, nyi
}
