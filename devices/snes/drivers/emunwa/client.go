package emunwa

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"net"
	"sni/cmd/sni/config"
	"sni/devices"
	"sni/devices/platforms"
	"sni/devices/snes/mapping"
	"sni/protos/sni"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	addr *net.TCPAddr
	name string

	lock        sync.Mutex
	c           *net.TCPConn
	isConnected bool
	isClosed    bool

	r *bufio.Reader

	muteLog bool

	readWriteTimeout time.Duration
	dialer           *net.Dialer

	currentCore     *CoreConfig
	currentPlatform *platforms.PlatformConf
	currentDomains  []*platforms.Domain
}

func (c *Client) FatalError(cause error) devices.DeviceError {
	return devices.DeviceFatal(fmt.Sprintf("emunwa: %v", cause), cause)
}

func (c *Client) NonFatalError(cause error) devices.DeviceError {
	return devices.DeviceNonFatal(fmt.Sprintf("emunwa: %v", cause), cause)
}

func NewClient(addr *net.TCPAddr, name string, timeout time.Duration) (c *Client) {
	c = &Client{
		addr:             addr,
		name:             name,
		readWriteTimeout: timeout,
		dialer:           &net.Dialer{Timeout: timeout},
	}

	return
}

func (c *Client) IsConnected() bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.isConnected
}
func (c *Client) IsClosed() bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.isClosed
}

func (c *Client) Connect() (err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.isClosed = false

	var conn net.Conn
	netAddr := net.Addr(c.addr)
	conn, err = c.dialer.Dial("tcp", netAddr.String())
	if err != nil {
		c.isConnected = false
		return
	}
	c.c = conn.(*net.TCPConn)

	c.r = bufio.NewReaderSize(c.c, 4096)
	c.isConnected = true
	return
}

func (c *Client) Close() (err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.isClosed = true
	c.isConnected = false
	err = c.c.Close()
	return
}

func (c *Client) DetectLoopback(others []*Client) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	for i := range others {
		other := others[i]

		if c.c == nil {
			continue
		}
		if other.c == nil {
			continue
		}

		// detect loopback condition:
		laddr := c.c.LocalAddr().(*net.TCPAddr)
		raddr := other.c.RemoteAddr().(*net.TCPAddr)
		if laddr.Port == raddr.Port {
			if laddr.IP.Equal(raddr.IP) {
				return true
			}
		}
	}

	return false
}

func (c *Client) MuteLog(mute bool) {
	c.muteLog = mute
}

func (c *Client) Logf(format string, args ...interface{}) {
	if c.muteLog {
		return
	}

	log.Printf("emunwa: "+format, args...)
}

func (c *Client) GetId() string {
	return c.name
}

func (c *Client) writeWithDeadline(bytes []byte, deadline time.Time) (err error) {
	err = c.c.SetWriteDeadline(deadline)
	if err != nil {
		err = c.FatalError(err)
		return
	}
	_, err = c.c.Write(bytes)
	if err != nil {
		err = c.FatalError(err)
		return
	}
	return
}

func (c *Client) SendCommandWaitReply(cmd string, deadline time.Time) (bin []byte, ascii []map[string]string, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	b := bytes.NewBuffer(make([]byte, 0, len(cmd)+1))
	b.WriteString(cmd)
	b.WriteByte('\n')

	if config.VerboseLogging {
		c.Logf("cmd> %s", b.Bytes())
	}

	err = c.writeWithDeadline(b.Bytes(), deadline)
	if err != nil {
		err = c.FatalError(err)
		return
	}

	bin, ascii, err = c.readResponse(deadline)
	if err != nil {
		return
	}
	if ascii != nil && len(ascii) > 0 {
		if errText, ok := ascii[0]["error"]; ok {
			err = fmt.Errorf("emunwa: error=%s", errText)
			return
		}
	}
	return
}

func (c *Client) SendCommandBinaryWaitReply(cmd string, binaryArg []byte, deadline time.Time) (bin []byte, ascii []map[string]string, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	b := bytes.NewBuffer(make([]byte, 0, 1+len(cmd)+2+4+len(binaryArg)))
	b.WriteByte('b')
	b.WriteString(cmd)
	b.WriteByte('\n')
	b.WriteByte(0)
	binary.Write(b, binary.BigEndian, uint32(len(binaryArg)))
	b.Write(binaryArg)

	if config.VerboseLogging {
		c.Logf("cmd> %#v", b.Bytes())
	}

	err = c.writeWithDeadline(b.Bytes(), deadline)
	if err != nil {
		err = c.FatalError(err)
		return
	}

	bin, ascii, err = c.readResponse(deadline)
	if ascii != nil && len(ascii) > 0 {
		if errText, ok := ascii[0]["error"]; ok {
			err = fmt.Errorf("emunwa: error=%s", errText)
			return
		}
	}
	return
}

func (c *Client) readResponse(deadline time.Time) (bin []byte, ascii []map[string]string, err error) {
	err = c.c.SetReadDeadline(deadline)
	if err != nil {
		err = c.FatalError(err)
		return
	}

	bin, ascii, err = c.parseResponse(c.r)
	return
}

func (c *Client) parseResponse(r *bufio.Reader) (bin []byte, ascii []map[string]string, err error) {
	var d byte
	d, err = r.ReadByte()
	if err != nil {
		err = c.FatalError(err)
		return
	}

	// parse binary reply:
	if d == 0 {
		var size uint32
		err = binary.Read(r, binary.BigEndian, &size)
		if err != nil {
			err = c.FatalError(err)
			return
		}

		bin = make([]byte, size)
		_, err = io.ReadFull(r, bin)
		if err != nil {
			err = c.FatalError(err)
			return
		}

		if config.VerboseLogging {
			c.Logf("bin< %s", hex.Dump(bin))
		}
		return
	}

	// expect ascii reply otherwise:
	if d != '\n' {
		err = fmt.Errorf("emunwa: command reply expected starting with '\\0' or '\\n' but got '%c'", d)
		return
	}

	// parse ascii reply as array<map<string,string>>:
	var b strings.Builder
	var sr io.Reader
	if config.VerboseLogging {
		// copy all bytes read to a string builder so we can log it after all scanned data:
		sr = io.TeeReader(r, &b)
	} else {
		sr = r
	}

	var s *bufio.Scanner
	s = bufio.NewScanner(sr)

	ascii = make([]map[string]string, 0, 4)
	item := make(map[string]string)
	for s.Scan() {
		l := s.Text()
		// empty line:
		if l == "" {
			break
		}

		pair := strings.SplitN(l, ":", 2)
		var key string = pair[0]
		var value string
		if len(pair) >= 2 {
			value = pair[1]
		}

		// duplicate keys delimit multiple items:
		if _, hasKey := item[key]; hasKey {
			ascii = append(ascii, item)
			item = make(map[string]string)
		}

		item[key] = value
	}

	if len(item) > 0 {
		ascii = append(ascii, item)
	}

	if config.VerboseLogging {
		c.Logf("asc<\n%s", b.String())
	}

	return
}

type memRegion struct {
	mapping.MemoryType
	Offset uint32
	Size   int
	Data   []byte
}

func (c *Client) RequiresMemoryMappingForAddressSpace(ctx context.Context, addressSpace sni.AddressSpace) (bool, error) {
	if addressSpace == sni.AddressSpace_Raw {
		return false, nil
	}
	if addressSpace == sni.AddressSpace_FxPakPro {
		return false, nil
	}
	return true, nil
}

func (c *Client) RequiresMemoryMappingForAddress(ctx context.Context, address devices.AddressTuple) (bool, error) {
	if address.AddressSpace == sni.AddressSpace_Raw {
		return false, nil
	}
	if address.AddressSpace == sni.AddressSpace_FxPakPro {
		return false, nil
	}
	return true, nil
}

func (c *Client) MultiReadMemory(ctx context.Context, reads ...devices.MemoryReadRequest) (mrsp []devices.MemoryReadResponse, err error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}

	mrsp = make([]devices.MemoryReadResponse, len(reads))

	// annoyingly, we must track the unique memType keys so we can iterate the map in a consistent order:
	memTypes := make([]mapping.MemoryType, 0, len(reads))
	readGroups := make(map[mapping.MemoryType][]memRegion)

	// divide up the reads into memory type groups:
	for j, read := range reads {
		memType, pakAddress, offset := mapping.MemoryTypeFor(read.RequestAddress)

		mrsp[j].RequestAddress = read.RequestAddress
		mrsp[j].DeviceAddress = devices.AddressTuple{
			Address:       pakAddress,
			AddressSpace:  sni.AddressSpace_FxPakPro,
			MemoryMapping: read.RequestAddress.MemoryMapping,
		}
		mrsp[j].DeviceAddress.Address = pakAddress
		mrsp[j].Data = make([]byte, read.Size)

		regions, ok := readGroups[memType]
		if !ok {
			memTypes = append(memTypes, memType)
		}
		readGroups[memType] = append(regions, memRegion{
			MemoryType: memType,
			Offset:     offset,
			Size:       read.Size,
			Data:       mrsp[j].Data,
		})
	}

	// write commands:
	c.lock.Lock()
	defer c.lock.Unlock()
	for _, memType := range memTypes {
		regions := readGroups[memType]
		sb := bytes.Buffer{}
		_, _ = fmt.Fprintf(&sb, "CORE_READ %s", memType)
		for _, region := range regions {
			_, _ = fmt.Fprintf(&sb, ";$%x;$%x", region.Offset, region.Size)
		}
		sb.WriteByte('\n')
		if config.VerboseLogging {
			c.Logf("cmd> %s", sb.Bytes())
		}
		err = c.writeWithDeadline(sb.Bytes(), deadline)
		if err != nil {
			return
		}
	}

	// read back responses:
	for _, memType := range memTypes {
		var bin []byte
		var ascii []map[string]string
		bin, ascii, err = c.readResponse(deadline)
		if err != nil {
			return
		}
		if ascii != nil {
			err = fmt.Errorf("emunwa: expecting binary reply but got ascii:\n%+v", ascii)
			return
		}

		regions := readGroups[memType]
		offset := 0
		for _, region := range regions {
			var sz = len(bin) - offset
			if offset >= len(bin) {
				// out of bounds
			} else if region.Size > sz {
				// partial read
				copy(region.Data, bin[offset:offset+sz])
			} else {
				// full read
				copy(region.Data, bin[offset:offset+region.Size])
			}
			offset += region.Size
		}
	}

	err = nil
	return
}

func (c *Client) MultiWriteMemory(ctx context.Context, writes ...devices.MemoryWriteRequest) (mrsp []devices.MemoryWriteResponse, err error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}

	mrsp = make([]devices.MemoryWriteResponse, len(writes))

	// annoyingly, we must track the unique memType keys so we can iterate the map in a consistent order:
	memTypes := make([]mapping.MemoryType, 0, len(writes))
	writeGroups := make(map[mapping.MemoryType][]memRegion)

	// divide up the writes into memory type groups:
	for j, write := range writes {
		memType, pakAddress, offset := mapping.MemoryTypeFor(write.RequestAddress)

		mrsp[j].RequestAddress = write.RequestAddress
		mrsp[j].DeviceAddress = devices.AddressTuple{
			Address:       pakAddress,
			AddressSpace:  sni.AddressSpace_FxPakPro,
			MemoryMapping: write.RequestAddress.MemoryMapping,
		}
		mrsp[j].DeviceAddress.Address = pakAddress
		mrsp[j].Size = len(write.Data)

		regions, ok := writeGroups[memType]
		if !ok {
			memTypes = append(memTypes, memType)
		}
		writeGroups[memType] = append(regions, memRegion{
			MemoryType: memType,
			Offset:     offset,
			Size:       len(write.Data),
			Data:       write.Data,
		})
	}

	// write commands:
	c.lock.Lock()
	defer c.lock.Unlock()
	for _, memType := range memTypes {
		regions := writeGroups[memType]

		// write command and build data buffer to send:
		sb := bytes.Buffer{}
		data := bytes.Buffer{}
		size := uint32(0)
		_, _ = fmt.Fprintf(&sb, "bCORE_WRITE %s", memType)
		for _, region := range regions {
			_, _ = fmt.Fprintf(&sb, ";$%x;$%x", region.Offset, region.Size)
			data.Write(region.Data)
			size += uint32(region.Size)
		}
		sb.WriteByte('\n')
		if config.VerboseLogging {
			c.Logf("cmd> %s", sb.Bytes())
		}
		err = c.writeWithDeadline(sb.Bytes(), deadline)
		if err != nil {
			return
		}

		// write data:
		sb.Reset()
		sb.WriteByte(0)
		_ = binary.Write(&sb, binary.BigEndian, size)
		sb.Write(data.Bytes())
		if config.VerboseLogging {
			c.Logf("bin> %s", hex.Dump(sb.Bytes()))
		}
		err = c.writeWithDeadline(sb.Bytes(), deadline)
		if err != nil {
			return
		}
	}

	// read replies:
	errReplies := strings.Builder{}
	for range memTypes {
		var ascii []map[string]string
		_, ascii, err = c.readResponse(deadline)
		if err != nil {
			return
		}
		if ascii != nil && len(ascii) > 0 {
			if errText, ok := ascii[0]["error"]; ok {
				errReplies.WriteString(errText)
				errReplies.WriteByte('\n')
			}
		}
	}

	if errReplies.Len() > 0 {
		err = fmt.Errorf("emunwa: error=%s", errReplies.String())
		err = c.NonFatalError(err)
		return
	}

	err = nil
	return
}

func clean(s string) string {
	return strings.TrimSpace(s)
}

func cleanLower(s string) string {
	return strings.ToLower(clean(s))
}

func (c *Client) determineDomainMapping(ctx context.Context) (err error) {
	if c.currentDomains != nil {
		return
	}

	var currentCore *CoreConfig = nil

	platConfig := currentPlatConfig
	if platConfig == nil {
		err = c.FatalError(fmt.Errorf("missing required platforms configuration"))
		return
	}

	// determine which platform is running:
	var coreName, coreVersion, corePlatform string
	var fields []string
	fields, err = c.FetchFields(ctx, sni.Field_CoreName, sni.Field_CoreVersion, sni.Field_CorePlatform)
	if err != nil {
		return
	}

	coreName, coreVersion, corePlatform = clean(fields[0]), clean(fields[1]), clean(fields[2])
	for _, coreConfig := range coresConfig {
		if !coreConfig.Matches.CoreNameRegex.Match([]byte(coreName)) {
			continue
		}
		if coreConfig.Matches.CoreVersionRegex != nil {
			if !coreConfig.Matches.CoreVersionRegex.Match([]byte(coreVersion)) {
				continue
			}
		}
		if coreConfig.Matches.CorePlatformRegex != nil {
			if !coreConfig.Matches.CorePlatformRegex.Match([]byte(corePlatform)) {
				continue
			}
		}

		// matched with all applicable regexps:
		currentCore = coreConfig
		break
	}

	if currentCore == nil {
		err = c.FatalError(fmt.Errorf(
			"could not find a match for coreName='%s',coreVersion='%s',corePlatform='%s' in platforms config",
			coreName,
			coreVersion,
			corePlatform,
		))
		return
	}

	var ok bool
	platformName := currentCore.Define.Platform
	var currentPlatform *platforms.PlatformConf
	currentPlatform, ok = platConfig.PlatformsByName[platformName]
	if !ok {
		err = c.FatalError(fmt.Errorf("could not find platform '%s' defined in platforms config", platformName))
		return
	}

	// now fetch exposed memory domain names for the core:
	deadline := c.computeDeadline(ctx)
	var memories []map[string]string
	_, memories, err = c.SendCommandWaitReply("CORE_MEMORIES", deadline)
	if err != nil {
		return
	}

	// build a list of platform memory domains, exposed and not:
	currentDomains := make([]*platforms.Domain, 0, len(currentPlatform.Domains)+len(memories))
	currentDomainsMap := make(map[string]*platforms.Domain, len(currentPlatform.Domains)+len(memories))
	for _, d := range currentPlatform.Domains {
		pd := &platforms.Domain{
			DomainConf:     *d,
			IsExposed:      false,
			IsCoreSpecific: false,
			IsReadable:     false,
			IsWriteable:    false,
		}
		currentDomains = append(currentDomains, pd)
		currentDomainsMap[cleanLower(pd.Name)] = pd
	}

	for _, m := range memories {
		coreMemName := cleanLower(m["name"])
		coreMemSize := cleanLower(m["size"])
		coreMemAccess := cleanLower(m["access"])

		var coreMemSizeN uint64
		coreMemSizeN, err = strconv.ParseUint(coreMemSize, 10, 64)
		if err != nil {
			err = c.FatalError(fmt.Errorf("could not parse integer size '%s' from nwa CORE_MEMORIES response", coreMemSize))
			return
		}

		var sniMemName string
		sniMemName, ok = currentCore.Define.CoreToSNIMapping[coreMemName]
		if !ok {
			err = c.FatalError(fmt.Errorf("could not map core memory name '%s' to SNI memory name", coreMemName))
			return
		}

		// get canonical SNI name from platform config:
		sniMemNameLower := cleanLower(sniMemName)
		var domainConf *platforms.DomainConf
		domainConf, ok = currentPlatform.DomainsByName[sniMemNameLower]
		if ok {
			sniMemName = domainConf.Name
			sniMemNameLower = cleanLower(sniMemName)
		}

		var pd *platforms.Domain
		pd, ok = currentDomainsMap[sniMemNameLower]
		if !ok {
			pd = &platforms.Domain{
				DomainConf: platforms.DomainConf{
					Name: sniMemName,
					Size: coreMemSizeN,
				},
				IsExposed:      true,
				IsCoreSpecific: true,
				IsReadable:     false,
				IsWriteable:    false,
			}
			currentDomains = append(currentDomains, pd)
			currentDomainsMap[sniMemNameLower] = pd
		}
		pd.IsExposed = true
		pd.Size = coreMemSizeN
		if coreMemAccess == "rw" {
			pd.IsReadable = true
			pd.IsWriteable = true
		} else if coreMemAccess == "r" {
			pd.IsReadable = true
		} else if coreMemAccess == "w" {
			pd.IsWriteable = true
		}
	}

	c.currentCore = currentCore
	c.currentPlatform = currentPlatform
	c.currentDomains = currentDomains
	return
}

func (c *Client) MemoryDomains(ctx context.Context, request *sni.MemoryDomainsRequest) (rsp *sni.MemoryDomainsResponse, err error) {
	// always fetch current core info and memory list:
	c.currentDomains, c.currentPlatform, c.currentDomains = nil, nil, nil
	err = c.determineDomainMapping(ctx)
	if err != nil {
		return
	}

	rsp = &sni.MemoryDomainsResponse{
		Uri:      request.Uri,
		CoreName: c.currentCore.Name,
		Domains:  make([]*sni.MemoryDomain, 0, len(c.currentDomains)),
	}

	for _, d := range c.currentDomains {
		rsp.Domains = append(rsp.Domains, &sni.MemoryDomain{
			Name:           d.Name,
			IsExposed:      d.IsExposed,
			IsCoreSpecific: d.IsCoreSpecific,
			Size:           d.Size,
			IsReadable:     d.IsReadable,
			IsWriteable:    d.IsWriteable,
		})
	}

	return
}

func (c *Client) MultiDomainRead(ctx context.Context, request *sni.MultiDomainReadRequest) (rsp *sni.MultiDomainReadResponse, err error) {
	// fetch domain mapping and core info if not already:
	err = c.determineDomainMapping(ctx)
	if err != nil {
		return
	}

	deadline := c.computeDeadline(ctx)

	mrsp := make([]*sni.GroupedDomainReadResponses, len(request.Requests))

	c.lock.Lock()
	defer c.lock.Unlock()

	// send CORE_READ commands:
	for i, req := range request.Requests {
		// reverse-lookup from SNI domain name to core domain name:
		var memName string
		var ok bool
		sniNameLower := cleanLower(req.Name)
		memName, ok = c.currentCore.Define.SNIToCoreMapping[sniNameLower]
		if !ok {
			err = c.NonFatalError(status.Errorf(codes.InvalidArgument, "unrecognized domain name '%s'", req.Name))
			return
		}

		// create a response struct to put the data into:
		mrsp[i] = &sni.GroupedDomainReadResponses{
			Name:  req.Name,
			Reads: make([]*sni.MemoryDomainOffsetData, len(req.Reads)),
		}

		// compose a CORE_READ command for this memory domain:
		sb := bytes.Buffer{}
		_, _ = fmt.Fprintf(&sb, "CORE_READ %s", memName)
		// add all the offset,size pairs:
		for j, read := range req.Reads {
			_, _ = fmt.Fprintf(&sb, ";$%x;$%x", read.Offset, read.Size)

			mrsp[i].Reads[j] = &sni.MemoryDomainOffsetData{
				Offset: read.Offset,
				Data:   make([]byte, read.Size),
			}
		}
		sb.WriteByte('\n')
		if config.VerboseLogging {
			c.Logf("cmd> %s", sb.Bytes())
		}
		err = c.writeWithDeadline(sb.Bytes(), deadline)
		if err != nil {
			return
		}
	}

	// receive CORE_READ responses:
	for i, req := range request.Requests {
		var bin []byte
		var ascii []map[string]string
		bin, ascii, err = c.readResponse(deadline)
		if err != nil {
			return
		}
		if ascii != nil {
			err = c.NonFatalError(fmt.Errorf("emunwa: expecting binary reply but got ascii:\n%+v", ascii))
			return
		}

		// copy binary response data into response:
		offset := uint64(0)
		binlen := uint64(len(bin))
		for j, region := range req.Reads {
			var sz = binlen - offset
			if offset >= binlen {
				// out of bounds
			} else if region.Size > sz {
				// partial read
				copy(mrsp[i].Reads[j].Data, bin[offset:offset+sz])
			} else {
				// full read
				copy(mrsp[i].Reads[j].Data, bin[offset:offset+region.Size])
			}
			offset += region.Size
		}
	}

	err = nil
	rsp = &sni.MultiDomainReadResponse{
		Uri:       request.Uri,
		Responses: mrsp,
	}
	return
}

func (c *Client) MultiDomainWrite(ctx context.Context, request *sni.MultiDomainWriteRequest) (rsp *sni.MultiDomainWriteResponse, err error) {
	// fetch domain mapping and core info if not already:
	err = c.determineDomainMapping(ctx)
	if err != nil {
		return
	}

	deadline := c.computeDeadline(ctx)

	mrsp := make([]*sni.GroupedDomainWriteResponses, len(request.Requests))

	c.lock.Lock()
	defer c.lock.Unlock()

	// send bCORE_WRITE commands
	for i, req := range request.Requests {
		// reverse-lookup from SNI domain name to core domain name:
		var memName string
		var ok bool
		sniNameLower := cleanLower(req.Name)
		memName, ok = c.currentCore.Define.SNIToCoreMapping[sniNameLower]
		if !ok {
			err = c.NonFatalError(status.Errorf(codes.InvalidArgument, "unrecognized domain name '%s'", req.Name))
			return
		}

		// create a response struct:
		mrsp[i] = &sni.GroupedDomainWriteResponses{
			Name:   req.Name,
			Writes: make([]*sni.MemoryDomainOffsetSize, len(req.Writes)),
		}

		sb := bytes.Buffer{}
		_, _ = fmt.Fprintf(&sb, "bCORE_WRITE %s", memName)
		// add all the offset,size pairs:
		var totalSize uint32 = 0
		for j, write := range req.Writes {
			size := uint64(len(write.Data))
			_, _ = fmt.Fprintf(&sb, ";$%x;$%x", write.Offset, size)

			mrsp[i].Writes[j] = &sni.MemoryDomainOffsetSize{
				Offset: write.Offset,
				Size:   size,
			}
			totalSize += uint32(size)
		}
		sb.WriteByte('\n')
		if config.VerboseLogging {
			c.Logf("cmd> %s", sb.Bytes())
		}

		// write binary payloads:
		sb.WriteByte(0)
		sizeBytes := [4]byte{}
		binary.BigEndian.PutUint32(sizeBytes[:], totalSize)
		sb.Write(sizeBytes[:])
		for _, write := range req.Writes {
			sb.Write(write.Data)
		}

		err = c.writeWithDeadline(sb.Bytes(), deadline)
		if err != nil {
			return
		}
	}

	// read responses:
	errReplies := strings.Builder{}
	for range request.Requests {
		var ascii []map[string]string
		_, ascii, err = c.readResponse(deadline)
		if err != nil {
			return
		}
		if ascii != nil && len(ascii) > 0 {
			if errText, ok := ascii[0]["error"]; ok {
				errReplies.WriteString(errText)
				errReplies.WriteByte('\n')
			}
		}
	}

	if errReplies.Len() > 0 {
		err = fmt.Errorf("emunwa: error=%s", errReplies.String())
		err = c.NonFatalError(err)
		return
	}

	rsp = &sni.MultiDomainWriteResponse{
		Uri:       request.Uri,
		Responses: mrsp,
	}
	err = nil
	return
}

func (c *Client) computeDeadline(ctx context.Context) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}
	return deadline
}

func (c *Client) ResetSystem(ctx context.Context) (err error) {
	deadline := c.computeDeadline(ctx)

	_, _, err = c.SendCommandWaitReply("EMULATION_RESET", deadline)
	return
}

func (c *Client) ResetToMenu(ctx context.Context) error {
	panic("implement me")
}

func (c *Client) PauseUnpause(ctx context.Context, pausedState bool) (newState bool, err error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}

	newState = pausedState
	if pausedState {
		_, _, err = c.SendCommandWaitReply("EMULATION_PAUSE", deadline)
	} else {
		_, _, err = c.SendCommandWaitReply("EMULATION_RESUME", deadline)
	}

	return
}

func (c *Client) PauseToggle(context.Context) (err error) {
	return fmt.Errorf("capability unavailable")
}

func (c *Client) NWACommand(ctx context.Context, cmd string, args string, binaryArg []byte) (asciiReply []map[string]string, binaryReply []byte, err error) {
	var line string
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}

	if binaryArg != nil {
		line = fmt.Sprintf("%s %s", cmd, args)
		binaryReply, asciiReply, err = c.SendCommandBinaryWaitReply(line, binaryArg, deadline)
	} else {
		line = fmt.Sprintf("%s %s", cmd, args)
		binaryReply, asciiReply, err = c.SendCommandWaitReply(line, deadline)
	}

	return
}

func getFirstValue(reply []map[string]string, name string) string {
	if len(reply) == 0 {
		return ""
	}
	return reply[0][name]
}

func (c *Client) FetchFields(ctx context.Context, fields ...sni.Field) (values []string, err error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.readWriteTimeout)
	}

	wantGameInfo := false        // RomFileName
	wantCoreInfo := false        // CoreName, CoreVersion, CorePlatform
	wantEmulatorInfo := false    // DeviceName, DeviceVersion
	wantEmulationStatus := false // DeviceStatus

	for _, field := range fields {
		switch field {
		case sni.Field_DeviceName:
		case sni.Field_DeviceVersion:
			wantEmulatorInfo = true
			break
		case sni.Field_DeviceStatus:
			wantEmulationStatus = true
			break
		case sni.Field_CoreName:
		case sni.Field_CoreVersion:
		case sni.Field_CorePlatform:
			wantCoreInfo = true
			break
		case sni.Field_RomFileName:
			wantGameInfo = true
			break
		}
	}

	var (
		gameInfo        []map[string]string
		coreInfo        []map[string]string
		emulatorInfo    []map[string]string
		emulationStatus []map[string]string
	)

	// make the necessary requests based on which fields are requested:
	if wantGameInfo {
		_, gameInfo, err = c.SendCommandWaitReply("GAME_INFO", deadline)
		if err != nil {
			return
		}
	}

	if wantCoreInfo {
		_, coreInfo, err = c.SendCommandWaitReply("CORE_CURRENT_INFO", deadline)
		if err != nil {
			return
		}
	}

	if wantEmulatorInfo {
		_, emulatorInfo, err = c.SendCommandWaitReply("EMULATOR_INFO", deadline)
		if err != nil {
			return
		}
	}

	if wantEmulationStatus {
		_, emulationStatus, err = c.SendCommandWaitReply("EMULATION_STATUS", deadline)
		if err != nil {
			return
		}
	}

	for _, field := range fields {
		switch field {
		case sni.Field_DeviceName:
			values = append(values, getFirstValue(emulatorInfo, "name"))
			break
		case sni.Field_DeviceVersion:
			values = append(values, getFirstValue(emulatorInfo, "version"))
			break
		case sni.Field_DeviceStatus:
			values = append(values, getFirstValue(emulationStatus, "state"))
			break
		case sni.Field_CoreName:
			values = append(values, getFirstValue(coreInfo, "name"))
			break
		case sni.Field_CoreVersion:
			values = append(values, getFirstValue(coreInfo, "version"))
			break
		case sni.Field_CorePlatform:
			values = append(values, getFirstValue(coreInfo, "platform"))
			break
		case sni.Field_RomFileName:
			values = append(values, getFirstValue(gameInfo, "file"))
			break
		default:
			values = append(values, "")
			break
		}
	}

	return
}
