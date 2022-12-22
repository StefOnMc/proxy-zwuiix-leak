package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"fyne.io/fyne/v2/widget"
	"github.com/pelletier/go-toml"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"golang.org/x/oauth2"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var players = map[string]*Player{}
var proxy = Proxy{hitbox: 0.6, heal: false, fly: false, antikb: false, jumpboost: false, speed: false, killaura: false, haste: false, slowfalling: false, noclip: false, nightvision: false}
var application App
var ProxyPrefix = "§9[§1P§9] §r§f"
var ProxyPrefixTip = "§9- "
var PREFIX = "/."
var PORT = 19133

type Player struct {
	name          string
	runtimeid     uint64
	uniqueid      int64
	dirtymetadata map[uint32]any
	metadata      map[uint32]any
}

type App struct {
	label *widget.Label
}

type Writter struct {
	w io.Writer
}

type Proxy struct {
	listener    *minecraft.Listener
	serverConn  *minecraft.Conn
	hitbox      float32
	fly         bool
	antikb      bool
	jumpboost   bool
	speed       bool
	killaura    bool
	haste       bool
	slowfalling bool
	noclip      bool
	nightvision bool
	heal bool
	reatch bool
}

func (Writter) Write(p []byte) (n int, err error) {
	str := string(p[:])
	fmt.Println(str)
	return len(p), nil
}

func main() {
	conf, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}
	PREFIX = conf.Settings.PREFIX
	startApp()
}

type config struct {
	Connection struct {
		DEFAULT string
		IP      string
		PORT    string
	}
	Settings struct {
		PREFIX string
	}
}

func readConfig() (config, error) {
	c := config{}
	c.Connection.DEFAULT = "0.0.0.0"
	c.Connection.IP = "vasar.land"
	c.Connection.PORT = "19132"
	c.Settings.PREFIX = "/."
	if _, err := os.Stat("config.toml"); os.IsNotExist(err) {
		data, err := toml.Marshal(c)
		if err != nil {
			return c, fmt.Errorf("failed encoding default config: %v", err)
		}
		if err := os.WriteFile("config.toml", data, 0644); err != nil {
			return c, fmt.Errorf("failed creating config: %v", err)
		}
		return c, nil
	}
	data, err := os.ReadFile("config.toml")
	if err != nil {
		return c, fmt.Errorf("error reading config: %v", err)
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("error decoding config: %v", err)
	}
	return c, nil
}

func startApp() {
	conf, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}
	startProxy(conf.Connection.IP, conf.Connection.PORT, ToInput("Mouse & Keyboard"))
}

func startProxy(ip string, port string, input int) {
	src := tokenSource()
	fmt.Println("Proxy running on.")

	p, err := minecraft.NewForeignStatusProvider(ip + ":" + port)
	if err != nil {
		panic(err)
	}
	proxy.listener, err = minecraft.ListenConfig{
		StatusProvider: p,
	}.Listen("raknet", "0.0.0.0:19132")
	if err != nil {
		panic(err)
	}
	defer proxy.listener.Close()
	for {
		c, err := proxy.listener.Accept()
		if err != nil {
			panic(err)
		}
		go handleConn(c.(*minecraft.Conn), src, ip, port, input)
	}
}

func startNewProxy(ip string, port string, input int) {
	src := tokenSource()
	fmt.Println("Proxy redirection.")

	p, err := minecraft.NewForeignStatusProvider(ip + ":" + port)
	if err != nil {
		panic(err)
	}
	proxy.listener, err = minecraft.ListenConfig{
		StatusProvider: p,
	}.Listen("raknet", "0.0.0.0:"+strconv.Itoa(PORT))
	if err != nil {
		panic(err)
	}
	defer proxy.listener.Close()
	for {
		c, err := proxy.listener.Accept()
		if err != nil {
			panic(err)
		}
		go handleConn(c.(*minecraft.Conn), src, ip, port, input)
	}
}

// tokenSource returns a token source for using with a gophertunnel client. It either reads it from the
// token.tok file if cached or requests logging in with a device code.
func tokenSource() oauth2.TokenSource {
	check := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	token := new(oauth2.Token)
	tokenData, err := ioutil.ReadFile("token.tok")
	if err == nil {
		_ = json.Unmarshal(tokenData, token)
	} else {
		token, err = auth.RequestLiveToken()
		check(err)
	}
	src := auth.RefreshTokenSource(token)
	_, err = src.Token()
	if err != nil {
		token, err = auth.RequestLiveToken()
		check(err)
		src = auth.RefreshTokenSource(token)
	}
	tok, _ := src.Token()
	b, _ := json.Marshal(tok)
	_ = ioutil.WriteFile("token.tok", b, 0644)
	return src
}

// handleConn handles a new incoming minecraft.Conn from the minecraft.Listener passed.
func handleConn(conn *minecraft.Conn, src oauth2.TokenSource, ip string, port string, input int) {
	clientdata := conn.ClientData()
	clientdata.CurrentInputMode = input
	var err error
	proxy.serverConn, err = minecraft.Dialer{
		TokenSource: src,
		ClientData:  clientdata,
	}.Dial("raknet", ip+":"+port)
	if err != nil {
		panic(err)
	}
	var g sync.WaitGroup
	g.Add(2)
	go func() {
		if err := conn.StartGame(proxy.serverConn.GameData()); err != nil {
			panic(err)
		}
		g.Done()
	}()
	go func() {
		if err := proxy.serverConn.DoSpawn(); err != nil {
			panic(err)
		}
		g.Done()
	}()
	g.Wait()

	go func() {
		defer proxy.listener.Disconnect(conn, "connection lost")
		defer proxy.serverConn.Close()
		for {
			pk, err := conn.ReadPacket()
			if err != nil {
				return
			}

			switch p := pk.(type) {
			case *packet.PlayerAuthInput:
				p.InputMode = uint32(input)
				break
			case *packet.RequestAbility:
				if p.Ability == protocol.AbilityFlying {
					if p.Value == proxy.fly {
						continue
					}
				}
				break
			case *packet.CommandRequest:
				var message = p.CommandLine
				var msg = strings.ToLower(message)
				var args = strings.Split(strings.TrimPrefix(msg, PREFIX), " ")
				var cmd = args[0]
				switch cmd {
				case "help":
					sendMessage(conn, `§9List of Commands`)
					sendMessage(conn, `§9`)
					sendMessage(conn, `§9Combat`)
					sendMessage(conn, `§1§l => §r§9hitbox`)
					sendMessage(conn, `§1§l => §r§9antikb`)
					sendMessage(conn, `§1§l => §r§9killaura`)
					sendMessage(conn, `§1§l => §r§9heal`)
					sendMessage(conn, `§9`)
					sendMessage(conn, `§9Others`)
					sendMessage(conn, `§1§l => §r§9gamemode <type>`)
					sendMessage(conn, `§1§l => §r§9haste`)
					sendMessage(conn, `§1§l => §r§9slowfalling`)
					sendMessage(conn, `§1§l => §r§9nightvision`)
					sendMessage(conn, `§1§l => §r§9speed`)
					sendMessage(conn, `§1§l => §r§9jumpboost`)
					continue
				case "antikb":
					if proxy.antikb {
						proxy.antikb = false
						desactivate(conn, "AntiKB")
					} else {
						proxy.antikb = true
						activate(conn, "AntiKB")

					}
					continue
				case "killaura":
					if proxy.killaura {
						proxy.killaura = false
						desactivate(conn, "KillAura")
					} else {
						proxy.killaura = true
						activate(conn, "KillAura")
					}
					continue
				case "gamemode":
					if len(args) < 3 && len(args) > 1 {
						switch args[1] {
						case "0":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeSurvival})
							sendMessage(conn, "§aSet own game mode to §9Survival§a !")
							continue
						case "s":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeSurvival})
							sendMessage(conn, "§aSet own game mode to §9Survival§a!")
							continue
						case "survival":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeSurvival})
							sendMessage(conn, "§aSet own game mode to §9Survival§a!")
							continue
						case "1":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeCreative})
							sendMessage(conn, "§aSet own game mode to §9Creative§a!")
							continue
						case "c":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeCreative})
							sendMessage(conn, "§aSet own game mode to §9Creative§a!")
							continue
						case "creative":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeCreative})
							sendMessage(conn, "§aSet own game mode to §9Creative§a!")
							continue
						case "2":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeAdventure})
							sendMessage(conn, "§aSet own game mode to §9Adventure§a!")
							continue
						case "a":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeAdventure})
							sendMessage(conn, "§aSet own game mode to §9Adventure§a!")
							continue
						case "adventure":
							_ = conn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeAdventure})
							sendMessage(conn, "§aSet own game mode to §9Adventure§a!")
							continue
						default:
							sendMessage(conn, "§cUnknown \""+args[1]+"\" game mode!")
							break
						}
					} else {
						sendMessage(conn, "§cUsage: "+PREFIX+"gamemode <mode>")
					}
					continue
				case "hitbox":
					if len(args) > 1 {
						nhitbox, err := strconv.ParseFloat(args[1], 32)
						if err != nil {
							panic(err)
						}
						proxy.hitbox = float32(nhitbox)
						if args[1] == "0" {
							for _, player := range players {
								player.dirtymetadata = player.metadata
								syncActor(conn, player.runtimeid, player.metadata)
							}
							desactivate(conn, "HitBox")
							continue
						}

						setHitbox(conn, float32(nhitbox))
						activate(conn, "HitBox §1"+args[1])
					}
					continue
				case "haste":
					if proxy.haste {
						proxy.haste = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      packet.EffectHaste,
							Amplifier:       2,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "Haste")
					} else {
						proxy.haste = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      packet.EffectHaste,
							Amplifier:       2,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "Haste")
					}
					continue
				case "heal":
					if proxy.heal {
						proxy.heal = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      10,
							Amplifier:       5,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "heal")
					} else {
						proxy.heal = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      10,
							Amplifier:       5,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "heal")
					}
					continue
				case "speed":
					if proxy.speed {
						proxy.speed = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      1,
							Amplifier:       2,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "Speed")
					} else {
						proxy.speed = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      1,
							Amplifier:       50,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "Speed")
					}
					continue
				case "jumpboost":
					if proxy.jumpboost {
						proxy.jumpboost = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      packet.EffectJumpBoost,
							Amplifier:       2,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "JumpBoost")
					} else {
						proxy.jumpboost = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      packet.EffectJumpBoost,
							Amplifier:       2,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "JumpBoost")
					}
					continue
				case "slowfalling":
					if proxy.slowfalling {
						proxy.slowfalling = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      27,
							Amplifier:       2,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "SlowFalling")
					} else {
						proxy.slowfalling = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      27,
							Amplifier:       2,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "SlowFalling")
					}
					continue
				case "nightvision":
					if proxy.nightvision {
						proxy.nightvision = false
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectRemove,
							EffectType:      packet.EffectNightVision,
							Amplifier:       2,
							Particles:       false,
							Duration:        1,
						})
						desactivate(conn, "NightVision")
					} else {
						proxy.nightvision = true
						_ = conn.WritePacket(&packet.MobEffect{
							EntityRuntimeID: conn.GameData().EntityRuntimeID,
							Operation:       packet.MobEffectAdd,
							EffectType:      packet.EffectNightVision,
							Amplifier:       2,
							Particles:       false,
							Duration:        999999999,
						})
						activate(conn, "NightVision")
					}
					continue
				}
			default:
				break
			}
			if err := proxy.serverConn.WritePacket(pk); err != nil {
				if disconnect, ok := errors.Unwrap(err).(minecraft.DisconnectError); ok {
					_ = proxy.listener.Disconnect(conn, disconnect.Error())
				}
				return
			}
		}
	}()
	go func() {
		// clientbound (server -> client)
		defer proxy.serverConn.Close()
		defer proxy.listener.Disconnect(conn, "connection lost")
		for {
			pk, err := proxy.serverConn.ReadPacket()
			if err != nil {
				if disconnect, ok := errors.Unwrap(err).(minecraft.DisconnectError); ok {
					_ = proxy.listener.Disconnect(conn, disconnect.Error())
				}
				return
			}
			switch p := pk.(type) {
			case *packet.AddPlayer:
				players[p.Username] = &Player{runtimeid: p.EntityRuntimeID, name: p.Username, metadata: p.EntityMetadata, dirtymetadata: p.EntityMetadata, uniqueid: p.AbilityData.EntityUniqueID}
				p.EntityMetadata[uint32(53)] = proxy.hitbox
				break
			case *packet.RemoveActor:
				for name, player := range players {
					if player.uniqueid == p.EntityUniqueID {
						delete(players, name)
						break
					}
				}
				break
			case *packet.SetActorData:
				for _, player := range players {
					if player.runtimeid == p.EntityRuntimeID {
						player.metadata = p.EntityMetadata
					}
				}
			case *packet.SetActorMotion:
				if p.EntityRuntimeID == conn.GameData().EntityRuntimeID {
					if proxy.antikb {
						continue
					}
				}
			case *packet.Transfer:
				conf, err := readConfig()
				if err != nil {
					log.Fatal(err)
				}
				PORT += 1
				port := PORT
				_ = conn.WritePacket(&packet.Transfer{
					Address: conf.Connection.DEFAULT,
					Port:    uint16(port),
				})
				startNewProxy(p.Address, strconv.Itoa(int(p.Port)), ToInput("Mouse & Keyboard"))
				continue
			case *packet.MoveActorAbsolute:
				pos := p.Position
				if proxy.killaura {
					go func() {
						_ = conn.WritePacket(&packet.InventoryTransaction{
							TransactionData: &protocol.UseItemOnEntityTransactionData{
								TargetEntityRuntimeID: p.EntityRuntimeID,
								ActionType:            protocol.UseItemOnEntityActionAttack,
								HotBarSlot:            0,
								HeldItem:              protocol.ItemInstance{},
								Position:              pos,
							},
						})
						time.Sleep(1 * time.Second)
					}()
				}
			default:
				break
			}
			if err := conn.WritePacket(pk); err != nil {
				return
			}
		}
	}()
}

func setHitbox(conn *minecraft.Conn, hitbox float32) {
	for _, p := range players {
		p.dirtymetadata[uint32(53)] = hitbox
		syncActor(conn, p.runtimeid, p.dirtymetadata)
	}
}

func syncActor(conn *minecraft.Conn, runtimeid uint64, metadata map[uint32]any) {
	_ = conn.WritePacket(&packet.SetActorData{EntityRuntimeID: runtimeid, EntityMetadata: metadata, Tick: 0})
}

func activate(conn *minecraft.Conn, resp string) {
	sendMessage(conn, "§aThe §9"+resp+"§a has been successfully activated!")
	sendTip(conn, "§aThe §9"+resp+"§a has been successfully activated! ")
}

func desactivate(conn *minecraft.Conn, resp string) {
	sendMessage(conn, "§cThe §9"+resp+"§c has been successfully deactivated!")
	sendTip(conn, "§cThe §9"+resp+"§c has been successfully deactivated! ")
}

func sendMessage(conn *minecraft.Conn, message string) {
	_ = conn.WritePacket(&packet.Text{
		TextType:         packet.TextTypeRaw,
		NeedsTranslation: false,
		SourceName:       "",
		Message:          ProxyPrefix + message,
		Parameters:       nil,
		XUID:             "",
		PlatformChatID:   "",
	})
}

func sendTip(conn *minecraft.Conn, message string) {
	_ = conn.WritePacket(&packet.Text{
		TextType:         packet.TextTypeTip,
		NeedsTranslation: false,
		SourceName:       "",
		Message:          ProxyPrefixTip + message + ProxyPrefixTip,
		Parameters:       nil,
		XUID:             "",
		PlatformChatID:   "",
	})
}

func sendPopup(conn *minecraft.Conn, message string) {
	_ = conn.WritePacket(&packet.Text{
		TextType:         packet.TextTypeJukeboxPopup,
		NeedsTranslation: false,
		SourceName:       "",
		Message:          ProxyPrefixTip + message + ProxyPrefixTip,
		Parameters:       nil,
		XUID:             "",
		PlatformChatID:   "",
	})
}

func loopbackExempted() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	data, _ := exec.Command("CheckNetIsolation", "LoopbackExempt", "-s", `-n="microsoft.minecraftuwp_8wekyb3d8bbwe"`).CombinedOutput()
	return bytes.Contains(data, []byte("microsoft.minecraftuwp_8wekyb3d8bbwe"))
}

func ToInput(input string) int {
	switch input {
	case "Mouse & Keyboard":
		return 1
	case "Touch":
		return 2
	case "Controller":
		return 3

	}
	return 0
}
