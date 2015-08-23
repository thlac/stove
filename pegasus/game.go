package pegasus

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"github.com/HearthSim/hs-proto-go/bnet/entity"
	"github.com/HearthSim/hs-proto-go/bnet/game_master_types"
	"github.com/HearthSim/hs-proto-go/pegasus/bobnet"
	"github.com/HearthSim/hs-proto-go/pegasus/game"
	"github.com/HearthSim/hs-proto-go/pegasus/shared"
	"github.com/HearthSim/stove/bnet"
	"github.com/golang/protobuf/proto"
	"log"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
)

type Game struct {
	sync.Mutex

	Player1     *Session
	Player1Deck Deck
	Player1Hero DbfCard

	Player2     *Session
	Player2Deck Deck
	Player2Hero DbfCard

	IsAIGame bool
	Address  net.TCPAddr

	Passwords map[string]*Session

	quit       chan struct{}
	sock       net.Listener
	kettleConn net.Conn
	clients    []*GameClient
	history    []*game.PowerHistoryData
}

func NewGame(player1, player2 *Session,
	player1DeckID, player2DeckID int,
	player1HeroCardID, player2HeroCardID int,
	isAIGame bool) *Game {

	res := &Game{}
	db.Preload("Cards").First(&res.Player1Deck, player1DeckID)
	db.Preload("Cards").First(&res.Player2Deck, player2DeckID)
	db.First(&res.Player1Hero, player1HeroCardID)
	db.First(&res.Player2Hero, player2HeroCardID)

	res.Player1 = player1
	res.Player2 = player2
	res.IsAIGame = isAIGame

	res.Passwords = map[string]*Session{}
	res.quit = make(chan struct{})
	return res
}

func (g *Game) Close() {
	g.sock.Close()
	for _, c := range g.clients {
		c.Disconnect()
	}
	g.Lock()
	defer g.Unlock()
	select {
	case <-g.quit:
	default:
		close(g.quit)
	}
}

func (g *Game) CloseOnError() {
	if err := recover(); err != nil {
		log.Printf("game server error: %v\n=== STACK TRACE ===\n%s",
			err, string(debug.Stack()))
		g.Close()
	}
}

func GenPassword() string {
	buf := make([]byte, 10)
	_, err := rand.Read(buf)
	if err != nil {
		panic(err)
	}
	// want printable ascii
	for i := 0; i < 10; i++ {
		buf[i] = 0x30 + (buf[i] & 0x3f)
	}
	return string(buf)
}

func (g *Game) Run() {
	defer g.CloseOnError()
	sock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		panic(err)
	}
	g.sock = sock
	go g.Listen()
	g.Address = *(sock.Addr().(*net.TCPAddr))

	player1Password := GenPassword()
	g.Passwords[player1Password] = g.Player1

	connectInfo := &game_master_types.ConnectInfo{}
	// TODO: figure out the right host
	connectInfo.Host = proto.String("127.0.0.1")
	connectInfo.Port = proto.Int32(int32(g.Address.Port))
	connectInfo.Token = []byte(player1Password)
	connectInfo.MemberId = &entity.EntityId{}
	connectInfo.MemberId.High = proto.Uint64(0)
	connectInfo.MemberId.Low = proto.Uint64(0)

	connectInfo.Attribute = bnet.NewNotification("", map[string]interface{}{
		"id":                 1,
		"game":               1,
		"resumable":          false,
		"spectator_password": "spectate",
		"version":            "3.0.0.9786",
	}).Attributes
	buf, err := proto.Marshal(connectInfo)
	if err != nil {
		panic(err)
	}
	g.Player1.gameNotifications <- bnet.NewNotification(bnet.NotifyQueueResult, map[string]interface{}{
		"connection_info": bnet.MessageValue{buf},
		"targetId":        *bnet.EntityId(0, 0),
		"forwardToClient": true,
	})

	g.history = append(g.history, &game.PowerHistoryData{
		CreateGame: &game.PowerHistoryCreateGame{},
	})

	kettle, err := net.Dial("tcp", "leclan.ch:9111")
	if err != nil {
		panic(err)
	}
	g.kettleConn = kettle

	init := KettleCreateGame{}
	player1Cards := []string{}
	for _, card := range g.Player1Deck.Cards {
		//for i := 0; i < card.Num; i++ {
		player1Cards = append(player1Cards, cardAssetIdToMiniGuid[card.CardID])
		//}
	}
	init.Players = append(init.Players, KettleCreatePlayer{
		Hero:  g.Player1Hero.NoteMiniGuid,
		Cards: player1Cards,
		Name:  "TestPlayer1",
	})
	player2Cards := []string{}
	for _, card := range g.Player2Deck.Cards {
		//for i := 0; i < card.Num; i++ {
		player2Cards = append(player2Cards, cardAssetIdToMiniGuid[card.CardID])
		//}
	}
	init.Players = append(init.Players, KettleCreatePlayer{
		Hero:  g.Player2Hero.NoteMiniGuid,
		Cards: player1Cards,
		Name:  "TestPlayer2",
	})
	init.GameID = "testing"
	initBuf, err := json.Marshal([]KettlePacket{{Type: "CreateGame", CreateGame: &init}})
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(initBuf)))
	if err != nil {
		panic(err)
	}
	written, err := kettle.Write(append(lenBuf, initBuf...))
	if err != nil {
		panic(err)
	}
	log.Printf("wrote %d bytes to kettle: %s\n", written, string(initBuf))
	for {
		for lenRead := 0; lenRead < 4; {
			read, err := kettle.Read(lenBuf[lenRead:])
			lenRead += read
			if err != nil {
				panic(err)
			}
		}
		plen := int(binary.LittleEndian.Uint32(lenBuf))
		packetBuf := make([]byte, plen)
		for packRead := 0; packRead < plen; {
			read, err := kettle.Read(packetBuf[packRead:])
			packRead += read
			if err != nil {
				panic(err)
			}
		}
		log.Printf("received %d bytes from kettle: %s...\n", len(packetBuf), string(packetBuf))
		packets := []KettlePacket{}
		json.Unmarshal(packetBuf, &packets)
		createGame := g.history[0].CreateGame
		playerId := 1
		for _, packet := range packets {
			switch packet.Type {
			case "GameEntity":
				log.Printf("got a %s!\n", packet.Type)
				createGame.GameEntity = packet.GameEntity.ToProto()
			case "Player":
				log.Printf("got a %s!\n", packet.Type)
				player := &game.Player{}
				player.Id = proto.Int32(int32(packet.Player.EntityID - 1))
				packet.Player.Tags["53"] = packet.Player.EntityID
				packet.Player.Tags["30"] = packet.Player.EntityID - 1
				player.Entity = packet.Player.ToProto()
				player.CardBack = proto.Int32(26)
				if playerId == 1 {
					player.GameAccountId = &shared.BnetId{
						Hi: proto.Uint64(144115193835963207),
						Lo: proto.Uint64(23658506),
					}
				} else {
					player.GameAccountId = &shared.BnetId{
						Hi: proto.Uint64(1234),
						Lo: proto.Uint64(5678),
					}
				}
				playerId++
				createGame.Players = append(createGame.Players, player)
			case "TagChange":
				log.Printf("got a %s!\n", packet.Type)
				tagChange := &game.PowerHistoryTagChange{}
				tag := MakeTag(packet.TagChange.Tag, packet.TagChange.Value)
				tagChange.Tag = tag.Name
				tagChange.Value = tag.Value
				tagChange.Entity = proto.Int32(int32(packet.TagChange.EntityID))
				g.history = append(g.history, &game.PowerHistoryData{
					TagChange: tagChange,
				})
			case "ActionStart":
				log.Printf("got a %s!\n", packet.Type)
			case "FullEntity":
				full := &game.PowerHistoryEntity{}
				packet.FullEntity.Tags["53"] = packet.FullEntity.EntityID
				e := packet.FullEntity.ToProto()
				full.Entity = e.Id
				full.Tags = e.Tags
				full.Name = proto.String(packet.FullEntity.CardID)
				g.history = append(g.history, &game.PowerHistoryData{
					FullEntity: full,
				})
				log.Printf("got a %s! %s\n", packet.Type, full.String())
			case "ShowEntity":
				log.Printf("got a %s!\n", packet.Type)
			case "HideEntity":
				log.Printf("got a %s!\n", packet.Type)
			case "MetaData":
				log.Printf("got a %s!\n", packet.Type)
			case "Choices":
				log.Printf("got a %s!\n", packet.Type)
			default:
				log.Panicf("unknown Kettle packet type: %s", packet.Type)
			}
		}
		for _, c := range g.clients {
			if c.hasHistoryTo < len(g.history) {
				c.packetQueue <- EncodePacket(game.PowerHistory_ID, &game.PowerHistory{
					List: g.history[c.hasHistoryTo:],
				})
				c.hasHistoryTo = len(g.history)
			}
		}
	}
}

type KettlePacket struct {
	Type        string
	CreateGame  *KettleCreateGame  `json:",omitempty"`
	GameEntity  *KettleEntity      `json:",omitempty"`
	Player      *KettlePlayer      `json:",omitempty"`
	TagChange   *KettleTagChange   `json:",omitempty"`
	ActionStart *KettleActionStart `json:",omitempty"`
	FullEntity  *KettleFullEntity  `json:",omitempty"`
	ShowEntity  *KettleFullEntity  `json:",omitempty"`
	HideEntity  *KettleFullEntity  `json:",omitempty"`
	MetaData    *KettleMetaData    `json:",omitempty"`
	Choices     *KettleChoices     `json:",omitempty"`
}

type KettleCreateGame struct {
	GameID  string
	Players []KettleCreatePlayer
}

type KettleCreatePlayer struct {
	Name  string
	Hero  string
	Cards []string
}

type GameTags map[string]int

type KettleEntity struct {
	EntityID int
	Tags     GameTags
}

func (e *KettleEntity) ToProto() *game.Entity {
	res := &game.Entity{}
	res.Id = proto.Int32(int32(e.EntityID))
	for name, value := range e.Tags {
		nameI, err := strconv.Atoi(name)
		if err != nil {
			panic(err)
		}
		t := MakeTag(nameI, value)
		res.Tags = append(res.Tags, t)
	}
	return res
}

func MakeTag(name, value int) *game.Tag {
	t := &game.Tag{}
	if name == 50 {
		value -= 1
	}
	t.Name = proto.Int32(int32(name))
	t.Value = proto.Int32(int32(value))
	return t
}

type KettlePlayer struct {
	KettleEntity
	PlayerID int
}

type KettleFullEntity struct {
	KettleEntity
	CardID string
}

type KettleTagChange struct {
	EntityID int
	Tag      int
	Value    int
}

type KettleActionStart struct {
	EntityID int
	SubType  int
	Index    int
	Target   int
}

type KettleMetaData struct {
	Meta int
	Data int
	Info int
}

type KettleChoices struct {
	Type     int
	EntityID int
	Source   int
	Min      int
	Max      int
	Choices  []int
}

type GameClient struct {
	sync.Mutex
	g            *Game
	s            *Session
	conn         net.Conn
	PlayerID     int
	packetQueue  chan *Packet
	quit         chan struct{}
	hasHistoryTo int
}

func (g *Game) Listen() {
	defer g.CloseOnError()
	log.Printf("Game server listening on %s...", g.sock.Addr().String())
	for {
		conn, err := g.sock.Accept()
		if err != nil {
			panic(err)
		}
		log.Printf("Game server handling client %s", conn.RemoteAddr().String())
		c := &GameClient{}
		c.g = g
		c.conn = conn
		c.packetQueue = make(chan *Packet, 1)
		c.quit = make(chan struct{})
		g.clients = append(g.clients, c)
		go c.ReadPackets()
		go c.WritePackets()
	}
}

func (c *GameClient) Disconnect() {
	c.conn.Close()
	c.Lock()
	defer c.Unlock()
	select {
	case <-c.quit:
	default:
		close(c.quit)
	}
}

func (c *GameClient) DisconnectOnError() {
	if err := recover(); err != nil {
		log.Printf("game session error: %v\n=== STACK TRACE ===\n%s",
			err, string(debug.Stack()))
		c.Disconnect()
	}
}

func (c *GameClient) ReadPackets() {
	defer c.DisconnectOnError()
	buf := make([]byte, 0x1000)
	for {
		_, err := c.conn.Read(buf[:8])
		if err != nil {
			log.Panicf("error: Game.ReadPackets: header read: %v", err)
		}
		packetID := binary.LittleEndian.Uint32(buf[:4])
		size := int(binary.LittleEndian.Uint32(buf[4:8]))
		if size > len(buf) {
			buf = append(buf, make([]byte, size-len(buf))...)
		}
		if size > 0 {
			_, err = c.conn.Read(buf[:size])
			if err != nil {
				log.Panicf("error: Game.ReadPackets: body read: %v", err)
			}
		}
		p := &Packet{}
		p.ID = int32(packetID)
		p.Body = buf[:size]
		c.HandlePacket(p)
	}
}

func (c *GameClient) WritePackets() {
	defer c.DisconnectOnError()
	for {
		select {
		case p := <-c.packetQueue:
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint32(buf[:4], uint32(p.ID))
			binary.LittleEndian.PutUint32(buf[4:8], uint32(len(p.Body)))
			buf = append(buf, p.Body...)
			_, err := c.conn.Write(buf)
			if err != nil {
				panic(err)
			}
			log.Printf("game session: wrote %d bytes\n", len(buf))
		case <-c.quit:
			return
		}
	}
}

type GameHandler func(c *GameClient, body []byte)

var gamePacketHandlers = map[int32]GameHandler{}

func OnGamePacket(x interface{}, h GameHandler) {
	gamePacketHandlers[packetIDFromProto(x).ID] = h
}

func init() {
	OnGamePacket(bobnet.AuroraHandshake_ID, (*GameClient).OnAuroraHandshake)
	OnGamePacket(bobnet.Ping_ID, (*GameClient).OnPing)
	OnGamePacket(game.GetGameState_ID, (*GameClient).OnGetGameState)
}

func (c *GameClient) HandlePacket(p *Packet) {
	log.Printf("handling game packet %d\n", p.ID)
	if h, ok := gamePacketHandlers[p.ID]; ok {
		h(c, p.Body)
	} else {
		log.Panicf("no game packet handler for ID = %d", p.ID)
	}
}

func (c *GameClient) OnAuroraHandshake(body []byte) {
	req := &bobnet.AuroraHandshake{}
	err := proto.Unmarshal(body, req)
	if err != nil {
		panic(err)
	}
	log.Printf("AuroraHandshake = %s", req.String())
	if sess, ok := c.g.Passwords[*req.Password]; ok {
		c.s = sess
		switch {
		case c.s == c.g.Player1:
			c.PlayerID = 1
		case c.s == c.g.Player2:
			c.PlayerID = 2
		}
	} else {
		panic("bad password")
	}

	gameSetup := &game.GameSetup{}
	gameSetup.Board = proto.Int32(8)
	gameSetup.MaxFriendlyMinionsPerPlayer = proto.Int32(7)
	gameSetup.MaxSecretsPerPlayer = proto.Int32(5)
	gameSetup.KeepAliveFrequency = proto.Int32(30)
	c.packetQueue <- EncodePacket(game.GameSetup_ID, gameSetup)
}

func (c *GameClient) OnPing(body []byte) {
	c.packetQueue <- EncodePacket(bobnet.Pong_ID, &bobnet.Pong{})
}

func (c *GameClient) OnGetGameState(body []byte) {
}
