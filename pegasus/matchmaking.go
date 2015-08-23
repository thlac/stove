package pegasus

import (
	"github.com/HearthSim/hs-proto-go/pegasus/shared"
	"github.com/HearthSim/stove/bnet"
	"log"
)

func (s *Session) HandleFindGame(req map[string]interface{}) {
	gameType := shared.BnetGameType(req["type"].(int64))
	deckID := req["deck"].(int64)
	scenario := &DbfScenario{}
	db.First(scenario, int(req["scenario"].(int64)))
	if scenario.ID == 0 {
		panic("bad scenario ID")
	}
	deck := Deck{}
	db.First(&deck, deckID)
	log.Printf("handling queue for scenario %v with type %s and deck %d\n",
		*scenario, gameType.String(), deckID)
	aiDeckID := 1
	if scenario.Players == 1 {
		game := NewGame(s, nil,
			int(deckID), aiDeckID,
			int(deck.HeroID), scenario.Player2HeroCardID,
			true)
		go game.Run()
	} else {
		panic(nyi)
	}
	s.gameNotifications <- bnet.NewNotification(bnet.NotifyFindGameResponse,
		map[string]interface{}{
			"queued":    true,
			"requestId": uint(1),
		})
}
