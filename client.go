package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

type StateType int32

const (
	StateDisconnected = iota
	StateTCPConnected
	StateELKConnected
)

type Client struct {
	conn         net.Conn
	state        StateType
	shareKey     []byte
	response     chan string
	condReady    *sync.Cond
	locker       sync.Mutex

	// Client information
	vendor    string
	model     string
	swversion string
	hdversion string
	sn        string
	ipaddr    string
	url       string
	wireless  string
}

func NewClient() Client {
	return Client{
		conn:         nil,
		state:        StateDisconnected,
		shareKey:     nil,
		response:     make(chan string, 100),
		condReady:    sync.NewCond(&sync.Mutex{}),
		locker:       sync.Mutex{},
	}
}

func (c *Client) sendData(data []byte) {
	msgStr := string(data)

	// Write magic code
	var msg bytes.Buffer
	msg.WriteByte(0x3f)
	msg.WriteByte(0x72)
	msg.WriteByte(0x1f)
	msg.WriteByte(0xb5)

	// Write length and body
	if c.shareKey != nil {
		b, err := AesEncrypt(data, c.shareKey)
		if err != nil {
			fmt.Println("Encrypt error:", err)
			return
		}

		l := len(b)
		msg.WriteByte(byte(l >> 24))
		msg.WriteByte(byte(l >> 16))
		msg.WriteByte(byte(l >> 8))
		msg.WriteByte(byte(l >> 0))
		msg.Write(b)
	} else {
		b := data
		l := len(b)
		msg.WriteByte(byte(l >> 24))
		msg.WriteByte(byte(l >> 16))
		msg.WriteByte(byte(l >> 8))
		msg.WriteByte(byte(l >> 0))
		msg.Write(b)
	}

	if c.conn != nil {
		if _, err := c.conn.Write(msg.Bytes()); err != nil {
			fmt.Println("[E]", msgStr, "Error:", err)
		} else {
			fmt.Println("[O]", msgStr)
		}
	}
}

func (c *Client) sendJSON(str string) {
	c.sendData([]byte(str))
}

// {
//   "type":"keyngreq",
//   "sequence":180,
//   "mac":"40C245300742",
//   "version":"V2017.1.0",
//   "keymodelist":[
//     {
//       "keymode":"dh"
//     }
//   ]
// }
func (c *Client) onMessageKEYNGREQ(sequence int32, mac string, msg interface{}) {
	c.sendJSON(fmt.Sprintf("{\"type\":\"keyngack\",\"mac\":\"%s\",\"sequence\":%d,\"keymode\":\"dh\"}",
		mac, sequence))
}

// {
//   "type":"dh",
//   "sequence":15,
//   "mac":"940E6B445754",
//   "data":{
//     "dh_key":"Nucd1a2mwzsQIJfcEI/TtQ==",
//     "dh_p":"3eeA2hvi1QBo7JF+Ful1Iw==",
//     "dh_g":"Ag=="
//   }
// }
func (c *Client) onMessageDH(sequence int32, mac string, msg interface{}) {
	var k string
	var p string
	var g string
	m := msg.(map[string]interface{})
	for key, val := range m {
		if key == "data" {
			data := val.(map[string]interface{})
			for kk, vv := range data {
				if kk == "dh_key" {
					k = vv.(string)
				} else if kk == "dh_p" {
					p = vv.(string)
				} else if kk == "dh_g" {
					g = vv.(string)
				}
			}
			break
		}
	}

	var bigK big.Int
	var bigP big.Int
	var bigG big.Int
	B64ToBigInt(k, &bigK)
	B64ToBigInt(p, &bigP)
	B64ToBigInt(g, &bigG)

	dh, _ := NewDH(rand.Reader, (128 + 7)/8, &bigG, &bigP)
	myPublicKey := dh.ComputePublic()
	sharedKey, _ := dh.ComputeShared(&bigK)

	//group := dhkx.CreateGroup(&bigP, &bigG)
	//privateKey, _ := group.GeneratePrivateKey(nil)
	//peerPublicKey := dhkx.NewPublicKey(bigK.Bytes())
	//sharedKey, _ := group.ComputeKey(peerPublicKey, privateKey)

	//var myPublicKey big.Int
	//myPublicKey.SetBytes(privateKey.Bytes())
	myKey := BigIntToB64(myPublicKey)
	c.sendJSON(fmt.Sprintf(
		"{\"type\":\"dh\",\"sequence\":%d,\"mac\":\"%s\",\"data\":{\"dh_key\":\"%s\",\"dh_p\":\"%s\",\"dh_g\":\"%s\"}}",
		sequence, mac, myKey, p, g))

	// Set shareKey here to avoid encrypt dh message
	c.shareKey = sharedKey.Bytes()
	fmt.Println("[I] GOT SHARED KEY:", c.shareKey)
}

// {
//   "type":"dev_reg",
//   "sequence":16,
//   "mac":"940E6B445754",
//   "data":{
//     "vendor":"HONOR",
//     "model":"CD28",
//     "swversion":"CD28-10-6.0.1.3_SP5_C30",
//     "hdversion":"VER.A",
//     "sn":"99230040013AA06876E000940E6B445754",
//     "ipaddr":"192.168.1.33",
//     "url":"",
//     "wireless":"no"
//   }
// }
func (c *Client) onMessageDEVREG(sequence int32, mac string, msg interface{}) {
	m := msg.(map[string]interface{})
	for key, val := range m {
		if key == "data" {
			data := val.(map[string]interface{})
			for kk, vv := range data {
				if kk == "vendor" {
					c.vendor = vv.(string)
				} else if kk == "model" {
					c.model = vv.(string)
				} else if kk == "swversion" {
					c.swversion = vv.(string)
				} else if kk == "hdversion" {
					c.hdversion = vv.(string)
				} else if kk == "sn" {
					c.sn = vv.(string)
				} else if kk == "ipaddr" {
					c.ipaddr = vv.(string)
				} else if kk == "url" {
					c.url = vv.(string)
				} else if kk == "wireless" {
					c.wireless = vv.(string)
				}
			}
			break
		}
	}

	c.sendJSON(fmt.Sprintf("{\"type\":\"ack\",\"sequence\":%d,\"mac\":\"%s\"}",
		sequence, mac))

	// The device is enrolled. the eLink connection can be used to send data.
	c.state = StateELKConnected
	c.condReady.Broadcast()
}

// {"type":"ack","sequence":16,"mac":"940E6B445754"}
func (c *Client) onMessageACK(sequence int32, mac string, msg interface{}) {
	// DO NOTHING
}

// {
//   "type":"status",
//   "sequence":10111,
//   "mac":"940E6B445754",
//   "status":{
//     "wifi": ...
//   }
// }
func (c *Client) onMessageSTATUS(sequence int32, mac string, msg interface{}) {
	// DO NOTHING
}

// {
//   "type": "dev_report",
//   "sequence": 14,
//   "mac": "E8BB3D11A0B5",
//   "dev": [
//     { "mac": "70:E7:2C:D5:86:01", "vmac": "192.168.101.235", "connecttype": 1 }
//   ]
// }
func (c *Client) onMessageDEVREPORT(sequence int32, mac string, msg interface{}) {
	c.sendJSON(fmt.Sprintf("{\"type\":\"ack\",\"sequence\":%d,\"mac\":\"%s\"}",
		sequence, mac))
}

// { "type": "keepalive", "sequence": 17, "mac": "E8BB3D11A0B5" }
func (c *Client) onMessageKEEPALIVE(sequence int32, mac string, msg interface{}) {
	c.sendJSON(fmt.Sprintf("{\"type\":\"ack\",\"sequence\":%d,\"mac\":\"%s\"}",
		sequence, mac))
}

func (c *Client) onMessageUnknown(sequence int32, mac string, msg interface{}) {
	// DO NOTHING
}

func (c *Client) onMessage(data []byte) {
	c.locker.Lock()
	defer c.locker.Unlock()

	// Decrypt the message at first
	if c.shareKey != nil {
		var err error
		data, err = AesDecrypt(data, c.shareKey)
		if err != nil {
			fmt.Println("[E] Error:", err)
		}
	}

	// Clear and show the message
	data = bytes.Trim(data, " \t\n\r\x00")
	fmt.Println("[I]", string(data))

	// Convert json string to object
	var msg interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Println("Decode message error: " + err.Error())
		return
	}

	// bool 代表 JSON booleans,
	// float64 代表 JSON numbers,
	// string 代表 JSON strings,
	// nil 代表 JSON null.
	var msgType string
	var msgSequence float64
	var msgMAC string
	m := msg.(map[string]interface{})
	for k, v := range m {
		if k == "type" {
			msgType = v.(string)
		} else if k == "sequence" {
			msgSequence = v.(float64)
		} else if k == "mac" {
			msgMAC = v.(string)
		}
	}

	if msgType == "keyngreq" {
		c.onMessageKEYNGREQ(int32(msgSequence), msgMAC, msg)
	} else if msgType == "dh" {
		c.onMessageDH(int32(msgSequence), msgMAC, msg)
	} else if msgType == "dev_reg" {
		c.onMessageDEVREG(int32(msgSequence), msgMAC, msg)
	} else if msgType == "ack" {
		c.onMessageACK(int32(msgSequence), msgMAC, msg)
	} else if msgType == "status" {
		c.onMessageSTATUS(int32(msgSequence), msgMAC, msg)
	} else if msgType == "dev_report" {
		c.onMessageDEVREPORT(int32(msgSequence), msgMAC, msg)
	} else if msgType == "keepalive" {
		c.onMessageKEEPALIVE(int32(msgSequence), msgMAC, msg)
	} else {
		c.onMessageUnknown(int32(msgSequence), msgMAC, msg)
	}

	c.response <- string(data)
}

func (c *Client) readLoop() {
	var buffer bytes.Buffer
	var messageLength int = 0
	for {
		var data = make([]byte, 1024)
		var err error
		var receivedBytes int
		if receivedBytes, err = c.conn.Read(data); err != nil {
			fmt.Println("Error:", err)
			break
		}
		buffer.Write(data[:receivedBytes])

		for buffer.Len() > 8 {
			if messageLength == 0 {
				data = buffer.Next(8)
				if data[0] != 0x3f || data[1] != 0x72 || data[2] != 0x1f || data[3] != 0xb5 {
					fmt.Printf("Received magic code error!\n")
					fmt.Printf("  EXP: 0x3F 0x72 0x1F 0xB5\n")
					fmt.Printf("  GOT: 0x%02X 0x%02X 0x%02X 0x%02X\n",
						data[0], data[1], data[2], data[3])
					break
				}

				messageLength |= int(data[4]) << 24
				messageLength |= int(data[5]) << 16
				messageLength |= int(data[6]) << 8
				messageLength |= int(data[7]) << 0
			}

			if buffer.Len() < messageLength {
				break
			}

			c.onMessage(buffer.Next(messageLength))
			messageLength = 0
		}
	}
}

func (c *Client) Run(conn net.Conn) {
	c.locker.Lock()
	defer c.locker.Unlock()

	if c.conn != nil {
		c.conn.Close()
	}
	c.conn = conn
	c.shareKey = nil
	c.state = StateTCPConnected
	go c.readLoop()
}

func (c *Client) WaitReady() {
	c.condReady.L.Lock()
	for c.state != StateELKConnected {
		c.condReady.Wait()
	}
	c.condReady.L.Unlock()
}

func (c *Client) SendRequest(msg interface{}) {
	c.locker.Lock()
	defer c.locker.Unlock()

	if d, err := json.Marshal(msg); err != nil {
		fmt.Println("Send message error:", err)
	} else {
		c.sendData(d)
	}
}

func (c *Client) WaitAndCheckResponse(seconds int, keywords []string) bool {
	timeout := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		select {
		case msg := <-c.response:
			// NOTE: `matched` default as true, so if `keywords` is empty
			// the result will be true
			matched := true
			for _, v := range keywords {
				if !strings.Contains(msg, v) {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		case <-time.After(timeout.Sub(time.Now())):
			return false
		}
	}
	return false
}

