package gozk

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	binarypack "github.com/canhlinh/go-binary-pack"
)

// Zk provides accesses to Zk machine fingerprint
type Zk interface {
	GetAttendances() ([]*Attendance, error)
	GetUsers() ([]*User, error)
	Connect() error
	Disconnect() error
}

// ZkSocket presents a Zk's socket
type ZkSocket struct {
	conn      net.Conn
	bp        *binarypack.BinaryPack
	sessionID int
	replyID   int
}

// NewZkSocket creates a new ZkSocket
func NewZkSocket(host string, port int) Zk {
	conn, err := net.DialTimeout("tcp", "192.168.0.201:4370", time.Second)
	if err != nil {
		panic(err)
	}

	return &ZkSocket{
		conn: conn,
		bp:   &binarypack.BinaryPack{},
	}
}

func (s *ZkSocket) createHeader(command int, commandString []byte, sessionID int, replyID int) ([]byte, error) {
	buf, err := s.bp.Pack([]string{"H", "H", "H", "H"}, []interface{}{command, 0, sessionID, replyID})
	if err != nil {
		return nil, err
	}

	buf = append(buf, commandString...)
	unpackPad := []string{
		"B", "B", "B", "B", "B", "B", "B", "B",
	}

	for i := 0; i < len(commandString); i++ {
		unpackPad = append(unpackPad, "B")
	}

	unpackBuf, err := s.bp.UnPack(unpackPad, buf)
	if err != nil {
		return nil, err
	}

	checksumBuf, err := s.createCheckSum(unpackBuf)
	if err != nil {
		return nil, err
	}

	c, err := s.bp.UnPack([]string{"H"}, checksumBuf)
	if err != nil {
		return nil, err
	}
	checksum := c[0].(int)

	replyID++
	if replyID >= USHRT_MAX {
		replyID -= USHRT_MAX
	}

	packData, err := s.bp.Pack([]string{"H", "H", "H", "H"}, []interface{}{command, checksum, sessionID, replyID})
	if err != nil {
		return nil, err
	}

	return append(packData, commandString...), nil
}

func (s *ZkSocket) createCheckSum(p []interface{}) ([]byte, error) {
	l := len(p)
	checksum := 0

	for l > 1 {
		pack, err := s.bp.Pack([]string{"B", "B"}, []interface{}{p[0], p[1]})
		if err != nil {
			return nil, err
		}

		unpack, err := s.bp.UnPack([]string{"H"}, pack)
		if err != nil {
			return nil, err
		}

		c := unpack[0].(int)
		checksum += c
		p = p[2:]

		if checksum > USHRT_MAX {
			checksum -= USHRT_MAX
		}
		l -= 2
	}

	if l > 0 {
		checksum = checksum + p[len(p)-1].(int)
	}

	for checksum > USHRT_MAX {
		checksum -= USHRT_MAX
	}

	checksum = ^checksum
	for checksum < 0 {
		checksum += USHRT_MAX
	}

	return s.bp.Pack([]string{"H"}, []interface{}{checksum})
}

func (s *ZkSocket) sendCommand(command int, commandString []byte, responseSize int) (*Response, error) {
	if commandString == nil {
		commandString = make([]byte, 0)
	}

	header, err := s.createHeader(command, commandString, s.sessionID, s.replyID)
	if err != nil {
		return nil, err
	}

	top, err := s.createTCPTop(header)
	if err != nil && err != io.EOF {
		return nil, err
	}

	if n, err := s.conn.Write(top); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, errors.New("Failed to write command")
	}

	s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	dataReceived := make([]byte, responseSize+8)

	bytesReceived, err := s.conn.Read(dataReceived)
	if err != nil && err != io.EOF {
		return nil, err
	}

	if bytesReceived < 16 {
		return nil, errors.New("Unknow error")
	}

	receivedHeader, err := s.bp.UnPack([]string{"H", "H", "H", "H"}, dataReceived[8:16])
	if err != nil {
		return nil, err
	}

	s.replyID = receivedHeader[3].(int)
	tcpLength := s.testTCPTop(dataReceived)
	dataReceived = dataReceived[16:bytesReceived]
	resCode := receivedHeader[0].(int)
	commandID := receivedHeader[2].(int)

	switch resCode {
	case CMD_ACK_OK, CMD_PREPARE_DATA, CMD_DATA:
		return &Response{
			Status:    true,
			Code:      resCode,
			TCPLength: tcpLength,
			Data:      dataReceived,
			CommandID: commandID,
		}, nil
	default:
		return &Response{
			Status:    false,
			Code:      resCode,
			TCPLength: tcpLength,
			Data:      dataReceived,
			CommandID: receivedHeader[2].(int),
		}, nil
	}
}

func (s *ZkSocket) createTCPTop(packet []byte) ([]byte, error) {
	top, err := s.bp.Pack([]string{"H", "H", "I"}, []interface{}{MACHINE_PREPARE_DATA_1, MACHINE_PREPARE_DATA_2, len(packet)})
	if err != nil {
		return nil, err
	}

	return append(top, packet...), nil
}

// Connect connects to the machine fingerprint
func (s *ZkSocket) Connect() error {
	s.sessionID = 0
	s.replyID = USHRT_MAX - 1

	res, err := s.sendCommand(CMD_CONNECT, nil, 8)
	if err != nil {
		return err
	}

	s.sessionID = res.CommandID

	return nil
}

// Disconnect disconnects out of the machine fingerprint
func (s *ZkSocket) Disconnect() error {
	if _, err := s.sendCommand(CMD_EXIT, nil, 8); err != nil {
		return err
	}

	return s.conn.Close()
}

func (s *ZkSocket) readWithBuffer(command, fct, ext int) ([]byte, int, error) {
	commandString, err := s.bp.Pack([]string{"b", "h", "i", "i"}, []interface{}{1, command, fct, ext})
	if err != nil {
		return nil, 0, err
	}

	res, err := s.sendCommand(1503, commandString, 1024)
	if err != nil {
		return nil, 0, err
	}

	if !res.Status {
		return nil, 0, errors.New("RWB Not supported")
	}

	if res.Code == CMD_DATA {

		if need := res.TCPLength - 8 - len(res.Data); need > 0 {
			moreData, err := s.receiveRawData(need)
			if err != nil {
				return nil, 0, err
			}

			data := append(res.Data, moreData...)
			return data, len(data), nil
		}

		return res.Data, len(res.Data), nil
	}

	sizeUnpack, err := s.bp.UnPack([]string{"I"}, res.Data[1:5])
	if err != nil {
		return nil, 0, err
	}

	size := sizeUnpack[0].(int)
	remain := size % MAX_CHUNK
	packets := (size - remain) / MAX_CHUNK

	data := []byte{}
	start := 0

	for i := 0; i < packets; i++ {

		d, err := s.readChunk(start, MAX_CHUNK)
		if err != nil {
			return nil, 0, err
		}
		data = append(data, d...)
		start += MAX_CHUNK
	}

	if remain > 0 {
		d, err := s.readChunk(start, remain)
		if err != nil {
			return nil, 0, err
		}

		data = append(data, d...)
		start += remain
	}

	s.freeData()
	return data, start, nil
}

func (s *ZkSocket) freeData() error {
	if _, err := s.sendCommand(CMD_FREE_DATA, nil, 0); err != nil {
		return err
	}

	return nil
}

func (s *ZkSocket) testTCPTop(packet []byte) int {
	if len(packet) <= 8 {
		return 0
	}

	tcpHeader, err := s.bp.UnPack([]string{"H", "H", "I"}, packet[:8])
	if err != nil {
		return 0
	}

	if tcpHeader[0].(int) == MACHINE_PREPARE_DATA_1 || tcpHeader[1].(int) == MACHINE_PREPARE_DATA_2 {
		return tcpHeader[2].(int)
	}

	return 0
}

// GetAttendances returns a list of attendances
func (s *ZkSocket) GetAttendances() ([]*Attendance, error) {

	records, err := s.readSize()
	if err != nil {
		return nil, err
	}

	data, size, err := s.readWithBuffer(CMD_ATTLOG_RRQ, 0, 0)
	if err != nil {
		return nil, err
	}

	if size < 4 {
		return []*Attendance{}, nil
	}

	totalSizeByte := data[:4]
	data = data[4:]

	totalSize := s.mustUnpack([]string{"I"}, totalSizeByte)[0].(int)
	recordSize := totalSize / records
	attendances := []*Attendance{}

	if recordSize == 8 || recordSize == 16 {
		return nil, errors.New("Sorry I don't support this kind of device. I'm lazy")

	}

	for len(data) >= 40 {
		v, err := s.bp.UnPack([]string{"H", "24s", "B", "4s", "B", "8s"}, data[:40])
		if err != nil {
			return nil, err
		}

		timestamp, err := s.decodeTime([]byte(v[3].(string)))
		if err != nil {
			return nil, err
		}

		userID, err := strconv.ParseInt(strings.Replace(v[1].(string), "\x00", "", -1), 10, 64)
		if err != nil {
			return nil, err
		}

		attendances = append(attendances, &Attendance{AttendedAt: timestamp, UserID: userID})
		data = data[40:]
	}

	return attendances, nil
}

func (s *ZkSocket) readSize() (int, error) {
	res, err := s.sendCommand(CMD_GET_FREE_SIZES, nil, 1024)
	if err != nil {
		return 0, err
	}

	if len(res.Data) >= 80 {
		pad := []string{}
		for i := 0; i < 20; i++ {
			pad = append(pad, "i")
		}
		return s.mustUnpack(pad, res.Data[:80])[8].(int), nil
	}

	return 0, nil
}

// GetUsers returns a list of users
// TODO: Not implemented yet
func (s *ZkSocket) GetUsers() ([]*User, error) {
	return nil, nil
}

func (s *ZkSocket) receiveRawData(size int) ([]byte, error) {
	data := []byte{}

	for size > 0 {
		chunkData := make([]byte, size)
		n, err := s.conn.Read(chunkData)
		if err != nil && err != io.EOF {
			return nil, err
		}

		data = append(data, chunkData[:n]...)
		size -= n
	}

	return data, nil
}

func (s *ZkSocket) readChunk(start, size int) ([]byte, error) {

	for i := 0; i < 3; i++ {
		commandString, err := s.bp.Pack([]string{"i", "i"}, []interface{}{start, size})
		if err != nil {
			return nil, err
		}

		res, err := s.sendCommand(CMD_READ_BUFFER, commandString, size+32)
		if err != nil {
			return nil, err
		}

		data, err := s.receiveChunk(res.Code, res.Data, res.TCPLength)
		if err != nil {
			return nil, err
		}

		return data, nil
	}

	return nil, errors.New("can't read chunk")
}

func (s *ZkSocket) receiveChunk(responseCode int, lastData []byte, tcpLength int) ([]byte, error) {

	switch responseCode {
	case CMD_DATA:
		if need := tcpLength - 8 - len(lastData); need > 0 {
			moreData, err := s.receiveRawData(need)
			if err != nil {
				return nil, err
			}
			return append(lastData, moreData...), nil
		}

		return lastData, nil
	case CMD_PREPARE_DATA:

		data := []byte{}
		size, err := s.getDataSize(responseCode, lastData)
		if err != nil {
			return nil, err
		}

		dataReceived := []byte{}
		if len(lastData) >= 8+size {
			dataReceived = lastData[8:]
		} else {
			dataReceived = append(lastData[8:], s.mustReceiveData(size+32)...)
		}

		d, brokenHeader, err := s.receiveTCPData(dataReceived, size)
		if err != nil {
			return nil, err
		}

		data = append(data, d...)

		if len(brokenHeader) < 16 {
			dataReceived = append(brokenHeader, s.mustReceiveData(16)...)
		} else {
			dataReceived = brokenHeader
		}

		if n := 16 - len(dataReceived); n > 0 {
			dataReceived = append(dataReceived, s.mustReceiveData(n)...)
		}

		unpack, err := s.bp.UnPack([]string{"H", "H", "H", "H"}, dataReceived[8:16])
		resCode := unpack[0].(int)

		if resCode == CMD_ACK_OK {
			return data, nil
		}

		return []byte{}, nil
	default:
		return nil, errors.New("Invalida reponse")
	}

}

func (s *ZkSocket) getDataSize(rescode int, data []byte) (int, error) {
	if rescode == CMD_PREPARE_DATA {
		sizeUnpack, err := s.bp.UnPack([]string{"I"}, data[:4])
		if err != nil {
			return 0, err
		}

		return sizeUnpack[0].(int), nil
	}

	return 0, nil
}

func (s *ZkSocket) receiveData(size int) ([]byte, error) {
	data := make([]byte, size)
	n, err := s.conn.Read(data)
	if err != nil {
		return nil, err
	}

	if n == 0 {
		return nil, errors.New("Failed to received DATA")
	}

	return data[:n-1], nil
}

func (s *ZkSocket) mustReceiveData(size int) []byte {
	data := make([]byte, size)
	n, err := s.conn.Read(data)
	if err != nil {
		panic(err)
	}

	if n == 0 {
		panic("Failed to receive data")
	}

	return data[:n]
}

func (s *ZkSocket) receiveTCPData(packet []byte, size int) ([]byte, []byte, error) {

	tcplength := s.testTCPTop(packet)
	data := []byte{}

	if tcplength <= 0 {
		return nil, data, errors.New("Incorrect tcp packet")
	}

	if n := (tcplength - 8); n < size {

		receivedData, brokenHeader, err := s.receiveTCPData(packet, n)
		if err != nil {
			return nil, nil, err
		}

		data = append(data, receivedData...)
		size -= len(receivedData)

		packet = append(packet, brokenHeader...)
		packet = append(packet, s.mustReceiveData(size+16)...)

		receivedData, brokenHeader, err = s.receiveTCPData(packet, size)
		if err != nil {
			return nil, nil, err
		}
		data = append(data, receivedData...)
		return data, brokenHeader, nil
	}

	packetSize := len(packet)
	responseCode := s.mustUnpack([]string{"H", "H", "H", "H"}, packet[8:16])[0].(int)

	if packetSize >= size+32 {
		if responseCode == CMD_DATA {
			return packet[16 : size+16], packet[size+16:], nil
		}

		return nil, nil, errors.New("Incorrect response")
	}

	if packetSize > size+16 {
		data = append(data, packet[16:size+16]...)
	} else {
		data = append(data, packet[16:packetSize]...)
	}

	size -= (packetSize - 16)
	brokenHeader := []byte{}

	if size < 0 {
		brokenHeader = packet[size:]
	} else if size > 0 {
		rawData, err := s.receiveRawData(size)
		if err != nil {
			return nil, nil, err
		}
		data = append(data, rawData...)
	}

	return data, brokenHeader, nil
}

func (s *ZkSocket) mustUnpack(pad []string, data []byte) []interface{} {
	value, err := s.bp.UnPack(pad, data)
	if err != nil {
		panic(err)
	}

	return value
}

func (s *ZkSocket) decodeTime(packet []byte) (time.Time, error) {
	unpack, err := s.bp.UnPack([]string{"I"}, packet)
	if err != nil {
		return time.Time{}, err
	}

	t := unpack[0].(int)

	second := t % 60
	t = t / 60

	minute := t % 60
	t = t / 60

	hour := t % 24
	t = t / 24

	day := t%31 + 1
	t = t / 31

	month := t%12 + 1
	t = t / 12

	year := t + 2000
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC), nil
}