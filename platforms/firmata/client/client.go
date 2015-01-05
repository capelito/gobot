package client

import (
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/hybridgroup/gobot"
)

// Pin Modes
const (
	Input  = 0x00
	Output = 0x01
	Analog = 0x02
	Pwm    = 0x03
	Servo  = 0x04
)

// Pin Levels
const (
	High = 0x01
	Low  = 0x00
)

// Sysex Codes
const (
	ProtocolVersion          byte = 0xF9
	SystemReset              byte = 0xFF
	DigitalMessage           byte = 0x90
	DigitalMessageRangeStart byte = 0x90
	DigitalMessageRangeEnd   byte = 0x9F
	AnalogMessage            byte = 0xE0
	AnalogMessageRangeStart  byte = 0xE0
	AnalogMessageRangeEnd    byte = 0xEF
	ReportAnalog             byte = 0xC0
	ReportDigital            byte = 0xD0
	PinMode                  byte = 0xF4
	StartSysex               byte = 0xF0
	EndSysex                 byte = 0xF7
	CapabilityQuery          byte = 0x6B
	CapabilityResponse       byte = 0x6C
	PinStateQuery            byte = 0x6D
	PinStateResponse         byte = 0x6E
	AnalogMappingQuery       byte = 0x69
	AnalogMappingResponse    byte = 0x6A
	StringData               byte = 0x71
	I2CRequest               byte = 0x76
	I2CReply                 byte = 0x77
	I2CConfig                byte = 0x78
	FirmwareQuery            byte = 0x79
	I2CModeWrite             byte = 0x00
	I2CModeRead              byte = 0x01
	I2CmodeContinuousRead    byte = 0x02
	I2CModeStopReading       byte = 0x03
)

type Client struct {
	Pins             []Pin
	FirmwareName     string
	ProtocolVersion  string
	interval         time.Duration
	mutex            *sync.Mutex
	disconnect       chan bool
	connected        bool
	connection       io.ReadWriteCloser
	analogPins       []int
	initTimeInterval time.Duration
	gobot.Eventer
}

type Pin struct {
	SupportedModes []int
	Mode           int
	Value          int
	AnalogChannel  int
}

// newBoard creates a new Client connected in specified serial port.
// Adds following events: "firmware_query", "capability_query",
// "analog_mapping_query", "report_version", "i2c_reply",
// "string_data", "firmware_query"
func New(conn io.ReadWriteCloser) *Client {
	c := &Client{
		ProtocolVersion: "",
		FirmwareName:    "",
		Pins:            []Pin{},
		Eventer:         gobot.NewEventer(),
		interval:        5 * time.Millisecond,
		mutex:           &sync.Mutex{},
		disconnect:      make(chan bool),
		connection:      conn,
		analogPins:      []int{},
		connected:       false,
	}

	for _, s := range []string{
		"FirmwareQuery",
		"CapabilityQuery",
		"AnalogMappingQuery",
		"ProtocolVersion",
		"I2cReply",
		"StringData",
		"Error",
	} {
		c.AddEvent(s)
	}

	return c
}

func (b *Client) Disconnect() (err error) {
	b.connected = false
	return b.connection.Close()
}

func (b *Client) Connected() bool {
	return b.connected
}

// connect starts connection to Client.
// Queries report version until connected
func (b *Client) Connect() (err error) {
	if !b.connected {
		initFunc := b.QueryProtocolVersion

		gobot.Once(b.Event("ProtocolVersion"), func(data interface{}) {
			initFunc = b.QueryFirmware
		})

		gobot.Once(b.Event("FirmwareQuery"), func(data interface{}) {
			initFunc = b.QueryCapabilities
		})

		gobot.Once(b.Event("CapabilityQuery"), func(data interface{}) {
			initFunc = b.QueryAnalogMapping
		})

		gobot.Once(b.Event("AnalogMappingQuery"), func(data interface{}) {
			initFunc = func() error { return nil }
			b.TogglePinReporting(0, High, ReportDigital)
			b.TogglePinReporting(1, High, ReportDigital)
			b.connected = true
		})

		for {
			if err := initFunc(); err != nil {
				return err
			}
			if err := b.Process(); err != nil {
				return err
			}
			if b.connected {
				go func() {
					for {
						if err := b.Process(); err != nil {
							gobot.Publish(b.Event("Error"), err)
						}
					}
				}()
				break
			}
		}
	}
	return
}

// reset writes system reset bytes.
func (b *Client) Reset() error {
	return b.write([]byte{SystemReset})
}

// setPinMode writes pin mode bytes for specified pin.
func (b *Client) SetPinMode(pin int, mode int) error {
	b.Pins[byte(pin)].Mode = mode
	return b.write([]byte{PinMode, byte(pin), byte(mode)})
}

// digitalWrite is used to send a digital value to a specified pin.
func (b *Client) DigitalWrite(pin int, value int) error {
	port := byte(math.Floor(float64(pin) / 8))
	portValue := byte(0)

	b.Pins[pin].Value = value

	for i := byte(0); i < 8; i++ {
		if b.Pins[8*port+i].Value != 0 {
			portValue = portValue | (1 << i)
		}
	}
	return b.write([]byte{DigitalMessage | port, portValue & 0x7F, (portValue >> 7) & 0x7F})
}

// analogWrite writes value to specified pin
func (b *Client) AnalogWrite(pin int, value int) error {
	b.Pins[pin].Value = value
	return b.write([]byte{AnalogMessage | byte(pin), byte(value & 0x7F), byte((value >> 7) & 0x7F)})
}

// queryFirmware writes bytes to query firmware from Client.
func (b *Client) QueryFirmware() error {
	return b.writeSysex([]byte{FirmwareQuery})
}

// queryPinState writes bytes to retrieve pin state
func (b *Client) QueryPinState(pin int) error {
	return b.writeSysex([]byte{PinStateQuery, byte(pin)})
}

// queryProtocolVersion sends query for report version
func (b *Client) QueryProtocolVersion() error {
	return b.write([]byte{ProtocolVersion})
}

// queryCapabilities is used to retrieve Client capabilities.
func (b *Client) QueryCapabilities() error {
	return b.writeSysex([]byte{CapabilityQuery})
}

// queryAnalogMapping returns analog mapping for Client.
func (b *Client) QueryAnalogMapping() error {
	return b.writeSysex([]byte{AnalogMappingQuery})
}

// togglePinReporting is used to change pin reporting mode.
func (b *Client) TogglePinReporting(pin int, state int, mode byte) error {
	return b.write([]byte{byte(mode) | byte(pin), byte(state)})
}

// i2cReadRequest reads from slaveAddress.
func (b *Client) I2cReadRequest(address int, numBytes int) error {
	return b.writeSysex([]byte{I2CRequest, byte(address), (I2CModeRead << 3),
		byte(numBytes) & 0x7F, (byte(numBytes) >> 7) & 0x7F})
}

// i2cWriteRequest writes to slaveAddress.
func (b *Client) I2cWriteRequest(address int, data []byte) error {
	ret := []byte{I2CRequest, byte(address), (I2CModeWrite << 3)}
	for _, val := range data {
		ret = append(ret, byte(val&0x7F))
		ret = append(ret, byte((val>>7)&0x7F))
	}
	return b.writeSysex(ret)
}

// i2xConfig returns i2c configuration.
func (b *Client) I2cConfig(data []byte) error {
	ret := []byte{I2CConfig}
	for _, val := range data {
		ret = append(ret, byte(val&0xFF))
		ret = append(ret, byte((val>>8)&0xFF))
	}
	return b.writeSysex(ret)
}

// write is used to send commands to serial port
func (b *Client) writeSysex(data []byte) (err error) {
	return b.write(append([]byte{StartSysex}, append(data, EndSysex)...))
}

// write is used to send commands to serial port
func (b *Client) write(data []byte) (err error) {
	_, err = b.connection.Write(data[:])
	return
}

func (b *Client) read(length int) (buf []byte, err error) {
	i := 0
	for length > 0 {
		tmp := make([]byte, length)
		if i, err = b.connection.Read(tmp); err != nil {
			if err.Error() != "EOF" {
				return
			}
			<-time.After(b.interval)
		}
		if i > 0 {
			buf = append(buf, tmp...)
			length = length - i
		}
	}
	return
}

// process uses incoming data and executes actions depending on what is received.
// The following messages are processed: reportVersion, AnalogMessageRangeStart,
// digitalMessageRangeStart.
// And the following responses: capability, analog mapping, pin state,
// i2c, firmwareQuery, string data.
// If neither of those messages is received, then data is treated as "bad_byte"
func (b *Client) Process() (err error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	buf, err := b.read(3)
	if err != nil {
		return err
	}
	messageType := buf[0]
	switch {
	case ProtocolVersion == messageType:
		b.ProtocolVersion = fmt.Sprintf("%v.%v", buf[1], buf[2])

		gobot.Publish(b.Event("ProtocolVersion"), b.ProtocolVersion)
	case AnalogMessageRangeStart <= messageType &&
		AnalogMessageRangeEnd >= messageType:

		value := uint(buf[1]) | uint(buf[2])<<7
		pin := (messageType & 0x0F)

		b.Pins[b.analogPins[pin]].Value = int(value)
		gobot.Publish(b.Event(fmt.Sprintf("AnalogRead%v", pin)),
			[]byte{
				byte(value >> 24),
				byte(value >> 16),
				byte(value >> 8),
				byte(value & 0xff),
			},
		)
	case DigitalMessageRangeStart <= messageType &&
		DigitalMessageRangeEnd >= messageType:

		port := messageType & 0x0F
		portValue := buf[1] | (buf[2] << 7)

		for i := 0; i < 8; i++ {
			pinNumber := (8*byte(port) + byte(i))
			pin := b.Pins[pinNumber]
			if byte(pin.Mode) == Input {
				pin.Value = int((portValue >> (byte(i) & 0x07)) & 0x01)
				gobot.Publish(b.Event(fmt.Sprintf("DigitalRead%v", pinNumber)),
					[]byte{byte(pin.Value & 0xff)})
			}
		}
	case StartSysex == messageType:
		currentBuffer := buf
		for {
			buf, err = b.read(1)
			if err != nil {
				return err
			}
			currentBuffer = append(currentBuffer, buf[0])
			if buf[0] == EndSysex {
				break
			}
		}
		command := currentBuffer[1]
		switch command {
		case CapabilityResponse:
			b.Pins = []Pin{}
			supportedModes := 0
			n := 0

			for _, val := range currentBuffer[2:(len(currentBuffer) - 5)] {
				if val == 127 {
					modes := []int{}
					for _, mode := range []int{Input, Output, Analog, Pwm, Servo} {
						if (supportedModes & (1 << byte(mode))) != 0 {
							modes = append(modes, mode)
						}
					}
					b.Pins = append(b.Pins, Pin{modes, Output, 0, 0})
					b.AddEvent(fmt.Sprintf("DigitalRead%v", len(b.Pins)-1))
					b.AddEvent(fmt.Sprintf("Pin%vState", len(b.Pins)-1))
					supportedModes = 0
					n = 0
					continue
				}

				if n == 0 {
					supportedModes = supportedModes | (1 << val)
				}
				n ^= 1
			}
			gobot.Publish(b.Event("CapabilityQuery"), nil)
		case AnalogMappingResponse:
			pinIndex := 0

			for _, val := range currentBuffer[2 : len(b.Pins)-1] {

				b.Pins[pinIndex].AnalogChannel = int(val)

				if val != 127 {
					b.analogPins = append(b.analogPins, pinIndex)
				}
				b.AddEvent(fmt.Sprintf("AnalogRead%v", pinIndex))
				pinIndex++
			}

			gobot.Publish(b.Event("AnalogMappingQuery"), nil)
		case PinStateResponse:
			pin := b.Pins[currentBuffer[2]]
			pin.Mode = int(currentBuffer[3])
			pin.Value = int(currentBuffer[4])

			if len(currentBuffer) > 6 {
				pin.Value = int(uint(pin.Value) | uint(currentBuffer[5])<<7)
			}
			if len(currentBuffer) > 7 {
				pin.Value = int(uint(pin.Value) | uint(currentBuffer[6])<<14)
			}

			gobot.Publish(b.Event(fmt.Sprintf("Pin%vState", currentBuffer[2])),
				map[string]int{
					"pin":   int(currentBuffer[2]),
					"mode":  int(pin.Mode),
					"value": int(pin.Value),
				},
			)
		case I2CReply:
			i2cReply := map[string][]byte{
				"slave_address": []byte{byte(currentBuffer[2]) | byte(currentBuffer[3])<<7},
				"register":      []byte{byte(currentBuffer[4]) | byte(currentBuffer[5])<<7},
				"data":          []byte{byte(currentBuffer[6]) | byte(currentBuffer[7])<<7},
			}
			for i := 8; i < len(currentBuffer); i = i + 2 {
				if currentBuffer[i] == byte(0xF7) {
					break
				}
				if i+2 > len(currentBuffer) {
					break
				}
				i2cReply["data"] = append(i2cReply["data"],
					byte(currentBuffer[i])|byte(currentBuffer[i+1])<<7,
				)
			}
			gobot.Publish(b.Event("I2cReply"), i2cReply)
		case FirmwareQuery:
			name := []byte{}
			for _, val := range currentBuffer[4:(len(currentBuffer) - 1)] {
				if val != 0 {
					name = append(name, val)
				}
			}
			b.FirmwareName = string(name[:])
			gobot.Publish(b.Event("FirmwareQuery"), b.FirmwareName)
		case StringData:
			str := currentBuffer[2:len(currentBuffer)]
			gobot.Publish(b.Event("StringData"), string(str[:len(str)]))
		default:
		}
	}
	return
}
