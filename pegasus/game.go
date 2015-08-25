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
	"io"
	"log"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
)

type Game struct {
	sync.Mutex

	Players []*GamePlayer

	HasBeenSetup bool
	IsAIGame     bool
	Address      net.TCPAddr

	Passwords map[string]*Session

	quit        chan struct{}
	sock        net.Listener
	kettleConn  net.Conn
	kettleWrite chan []byte
	clients     []*GameClient
	history     []*game.PowerHistoryData
}

type GamePlayer struct {
	s        *Session
	Name     string
	Deck     Deck
	Hero     DbfCard
	Password string
}

func NewGame(player1, player2 *Session,
	playerDeckIds [2]int, playerHeroCardIds [2]int,
	isAIGame bool) *Game {

	res := &Game{}
	res.Players = make([]*GamePlayer, 2)
	for i := 0; i < 2; i++ {
		res.Players[i] = &GamePlayer{}
		p := res.Players[i]
		db.Preload("Cards").First(&p.Deck, playerDeckIds[i])
		db.First(&p.Hero, playerHeroCardIds[i])
	}

	res.Players[0].s = player1
	res.Players[1].s = player2
	res.Players[0].Name = "Player1"
	res.Players[1].Name = "Player2"
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
	g.kettleConn.Close()
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

	for _, p := range g.Players {
		password := GenPassword()
		g.Passwords[password] = p.s
		p.Password = password
	}

	connectInfo := &game_master_types.ConnectInfo{}
	// TODO: figure out the right host
	connectInfo.Host = proto.String("127.0.0.1")
	connectInfo.Port = proto.Int32(int32(g.Address.Port))
	connectInfo.Token = []byte(g.Players[0].Password)
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
	g.Players[0].s.gameNotifications <- bnet.NewNotification(bnet.NotifyQueueResult, map[string]interface{}{
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

	lenBuf := make([]byte, 4)
	g.kettleWrite = make(chan []byte, 1)
	go g.WriteKettle()
	g.InitKettle()
	for {
		_, err := io.ReadFull(kettle, lenBuf)
		if err != nil {
			panic(err)
		}
		plen := int(binary.LittleEndian.Uint32(lenBuf))
		packetBuf := make([]byte, plen)
		_, err = io.ReadFull(kettle, packetBuf)
		if err != nil {
			panic(err)
		}

		log.Printf("received %d bytes from kettle: %s\n", len(packetBuf), string(packetBuf))
		packets := []KettlePacket{}
		json.Unmarshal(packetBuf, &packets)
		createGame := g.history[0].CreateGame
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
				if len(createGame.Players) == 0 {
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
				if !g.HasBeenSetup {
					for _, p := range packets {
						if p.Type == "TagChange" &&
							p.TagChange.EntityID == packet.FullEntity.EntityID &&
							p.TagChange.Tag == 49 {
							packet.FullEntity.Tags["49"] = p.TagChange.Value
						}
					}
				}
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
			case "Options":
				log.Printf("got a %s!\n", packet.Type)
				options := &game.AllOptions{}
				options.Id = proto.Int32(1)
				for _, o := range packet.Options {
					options.Options = append(options.Options, o.ToProto())
				}
				for _, c := range g.clients {
					c.packetQueue <- EncodePacket(game.AllOptions_ID, options)
				}
			default:
				log.Panicf("unknown Kettle packet type: %s", packet.Type)
			}
		}
		g.HasBeenSetup = true
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

func (g *Game) InitKettle() {
	init := KettleCreateGame{}
	for _, player := range g.Players {
		playerCards := []string{}
		for _, card := range player.Deck.Cards {
			//for i := 0; i < card.Num; i++ {
			playerCards = append(playerCards, cardAssetIdToMiniGuid[card.CardID])
			//}
		}
		init.Players = append(init.Players, KettleCreatePlayer{
			Hero:  player.Hero.NoteMiniGuid,
			Cards: playerCards,
			Name:  player.Name,
		})
	}
	initBuf, err := json.Marshal([]KettlePacket{{
		Type:       "CreateGame",
		GameID:     "testing",
		CreateGame: &init,
	}})
	if err != nil {
		panic(err)
	}
	g.kettleWrite <- initBuf
}

func (g *Game) WriteKettle() {
	defer g.CloseOnError()
	for {
		select {
		case buf := <-g.kettleWrite:
			lenBuf := make([]byte, 4)
			binary.LittleEndian.PutUint32(lenBuf, uint32(len(buf)))
			written, err := g.kettleConn.Write(append(lenBuf, buf...))
			if err != nil {
				panic(err)
			}
			log.Printf("wrote %d bytes to kettle: %s\n", written, string(buf))
		case <-g.quit:
			return
		}
	}
}

type KettlePacket struct {
	Type        string
	GameID      string
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
	Options     []*KettleOption    `json:",omitempty"`
}

type KettleCreateGame struct {
	Players []KettleCreatePlayer
}

type KettleCreatePlayer struct {
	Name  string
	Hero  string
	Cards []string
}

type GameTags map[string]int

func TagsToProto(tags GameTags) []*game.Tag {
	res := []*game.Tag{}
	for name, value := range tags {
		nameI, err := strconv.Atoi(name)
		if err != nil {
			panic(err)
		}
		t := MakeTag(nameI, value)
		res = append(res, t)
	}
	return res
}

type KettleEntity struct {
	EntityID int
	Tags     GameTags
}

func (e *KettleEntity) ToProto() *game.Entity {
	res := &game.Entity{}
	res.Id = proto.Int32(int32(e.EntityID))
	res.Tags = TagsToProto(e.Tags)
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

type KettleOption struct {
	Type       int
	MainOption *KettleSubOption   `json:",omitempty"`
	SubOptions []*KettleSubOption `json:",omitempty"`
}

type KettleSubOption struct {
	ID      int
	Targets []int `json:",omitempty"`
}

func (o *KettleOption) ToProto() *game.Option {
	res := &game.Option{}
	var x = game.Option_Type(o.Type)
	res.Type = &x
	if o.MainOption != nil {
		res.MainOption = o.MainOption.ToProto()
	}
	for _, s := range o.SubOptions {
		res.SubOptions = append(res.SubOptions, s.ToProto())
	}
	return res
}

func (s *KettleSubOption) ToProto() *game.SubOption {
	res := &game.SubOption{}
	res.Id = proto.Int32(int32(s.ID))
	for _, t := range s.Targets {
		res.Targets = append(res.Targets, int32(t))
	}
	return res
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
		if c.s != nil {
			c.g.Close()
		}
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
	OnGamePacket(game.ChooseOption_ID, (*GameClient).OnChooseOption)
	OnGamePacket(game.UserUI_ID, (*GameClient).OnUserUI)
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
		for i, p := range c.g.Players {
			if p.s == c.s {
				c.PlayerID = i + 1
				break
			}
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

func (c *GameClient) OnGetGameState(body []byte) {}

func (c *GameClient) OnChooseOption(body []byte) {
	req := &game.ChooseOption{}
	proto.Unmarshal(body, req)
	log.Printf("ChooseOption = %s\n", req.String())
}

func (c *GameClient) OnUserUI(body []byte) {
	req := &game.UserUI{}
	proto.Unmarshal(body, req)
	log.Printf("UserUI = %s\n", req.String())
}
