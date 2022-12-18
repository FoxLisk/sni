package fxpakpro

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sni/devices"
	"sni/protos/sni"
	"strings"
)

type domainDesc struct {
	domainRef *sni.MemoryDomainRef
	notes     string
	start     uint32
	size      uint32
	writeable bool
}

func s(v string) *string {
	return &v
}

var domainRefs = [...]sni.MemoryDomainRef_Snes{
	{Snes: sni.MemoryDomainTypeSNES_SNESCoreSpecificMemory},

	{Snes: sni.MemoryDomainTypeSNES_SNESCartROM},
	{Snes: sni.MemoryDomainTypeSNES_SNESCartRAM},
	{Snes: sni.MemoryDomainTypeSNES_SNESWorkRAM},
	{Snes: sni.MemoryDomainTypeSNES_SNESAPURAM},
	{Snes: sni.MemoryDomainTypeSNES_SNESVideoRAM},
	{Snes: sni.MemoryDomainTypeSNES_SNESCGRAM},
	{Snes: sni.MemoryDomainTypeSNES_SNESObjectAttributeMemory},
}

var domainDescs = [...]domainDesc{
	{
		domainRef: &sni.MemoryDomainRef{Name: s("CARTROM"), Type: &domainRefs[1]},
		notes:     "Cartridge ROM",
		size:      0xE0_0000,
		writeable: true,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("CARTRAM"), Type: &domainRefs[2]},
		notes:     "Cartridge battery-backed RAM aka SRAM",
		start:     0xE0_0000,
		size:      0x10_0000,
		writeable: true,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("WRAM"), Type: &domainRefs[3]},
		notes:     "Console Work RAM",
		start:     0xF5_0000,
		size:      0x02_0000,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("APURAM"), Type: &domainRefs[4]},
		notes:     "Console APU RAM",
		start:     0xF8_0000,
		size:      0x01_0000,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("VRAM"), Type: &domainRefs[5]},
		notes:     "Console Video RAM",
		start:     0xF7_0000,
		size:      0x01_0000,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("CGRAM"), Type: &domainRefs[6]},
		notes:     "Console Video Palette RAM",
		start:     0xF9_0000,
		size:      0x0200,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("OAM"), Type: &domainRefs[7]},
		notes:     "Console Object Attribute Memory",
		start:     0xF9_0200,
		size:      0x0420 - 0x0200,
	},
	// core-specific memory:
	{
		domainRef: &sni.MemoryDomainRef{Name: s("FXPAKPRO_SNES"), Type: &domainRefs[0]},
		notes:     "Entire FX Pak Pro address space for SNES space",
		size:      0x100_0000,
		writeable: true,
	},
	{
		domainRef: &sni.MemoryDomainRef{Name: s("FXPAKPRO_CMD"), Type: &domainRefs[0]},
		notes:     "Entire FX Pak Pro address space for CMD space",
		start:     0x100_0000,
		size:      0x100_0000,
		writeable: true,
	},
}
var domains []*sni.MemoryDomain
var domainDescByType map[sni.MemoryDomainTypeSNES]*domainDesc
var domainDescByName map[string]*domainDesc

func init() {
	domains = make([]*sni.MemoryDomain, 0, len(domainDescs))
	domainDescByType = make(map[sni.MemoryDomainTypeSNES]*domainDesc, len(domainDescs))
	domainDescByName = make(map[string]*domainDesc, len(domainDescs))

	for i := range domainDescs {
		d := &domainDescs[i]
		domains = append(domains, &sni.MemoryDomain{
			Core:      driverName,
			Domain:    d.domainRef,
			Notes:     d.notes,
			Size:      d.size,
			Readable:  true,
			Writeable: d.writeable,
		})
		domainDescByType[d.domainRef.Type.(*sni.MemoryDomainRef_Snes).Snes] = d
		domainDescByName[*d.domainRef.Name] = d
	}
}

func (d *Device) MemoryDomains(_ context.Context, request *sni.MemoryDomainsRequest) (rsp *sni.MemoryDomainsResponse, err error) {
	rsp = &sni.MemoryDomainsResponse{
		Uri:     request.Uri,
		Domains: domains,
	}

	return
}

func (d *Device) MultiDomainRead(ctx context.Context, request *sni.MultiDomainReadRequest) (rsp *sni.MultiDomainReadResponse, err error) {
	mreqs := make([]devices.MemoryReadRequest, 0)
	rsp = &sni.MultiDomainReadResponse{
		Responses: make([]*sni.GroupedDomainReadResponses, len(request.Requests)),
	}
	addressDatas := make([]*sni.MemoryDomainAddressData, 0)

	for i, domainReqs := range request.Requests {
		snesDomainRef, ok := domainReqs.Domain.Type.(*sni.MemoryDomainRef_Snes)
		if !ok {
			err = status.Errorf(codes.InvalidArgument, "Domain.Type must be of SNES type")
			return
		}

		var dm *domainDesc
		if snesDomainRef.Snes == sni.MemoryDomainTypeSNES_SNESCoreSpecificMemory {
			// look up by string name instead:
			if domainReqs.Domain.Name == nil {
				err = status.Error(codes.InvalidArgument, "domain name must be non-nil when using SNESCoreSpecificMemory type")
				return
			}

			domainName := strings.ToUpper(*domainReqs.Domain.Name)
			dm, ok = domainDescByName[domainName]
			if !ok {
				err = status.Errorf(codes.InvalidArgument, "invalid domain name '%s'", domainName)
				return
			}
		} else {
			dm, ok = domainDescByType[snesDomainRef.Snes]
			if !ok {
				err = status.Errorf(codes.InvalidArgument, "invalid domain type '%s'", snesDomainRef.Snes)
				return
			}
		}

		rsp.Responses[i] = &sni.GroupedDomainReadResponses{
			Domain: dm.domainRef,
			Reads:  make([]*sni.MemoryDomainAddressData, len(domainReqs.Reads)),
		}
		for j, read := range domainReqs.Reads {
			if read.Address >= dm.size {
				err = status.Errorf(codes.InvalidArgument, "request start address 0x%06x exceeds domain %s size 0x%06x", read.Address, *dm.domainRef.Name, dm.size)
				return
			}
			if read.Address+read.Size > dm.size {
				err = status.Errorf(codes.InvalidArgument, "request end address 0x%06x exceeds domain %s size 0x%06x", read.Address+read.Size, *dm.domainRef.Name, dm.size)
				return
			}

			mreq := devices.MemoryReadRequest{
				RequestAddress: devices.AddressTuple{
					Address:       dm.start + read.Address,
					AddressSpace:  sni.AddressSpace_FxPakPro,
					MemoryMapping: sni.MemoryMapping_Unknown,
				},
				Size: int(read.Size),
			}
			mreqs = append(mreqs, mreq)

			addressData := &sni.MemoryDomainAddressData{
				Address: read.Address,
				Data:    nil,
			}
			addressDatas = append(addressDatas, addressData)
			rsp.Responses[i].Reads[j] = addressData
		}
	}

	var mrsp []devices.MemoryReadResponse
	mrsp, err = d.MultiReadMemory(ctx, mreqs...)
	if err != nil {
		return
	}

	for k := range mrsp {
		// update the `GroupedDomainReadResponses_AddressData`s across the groupings:
		addressDatas[k].Data = mrsp[k].Data
	}

	return
}

func (d *Device) MultiDomainWrite(ctx context.Context, request *sni.MultiDomainWriteRequest) (rsp *sni.MultiDomainWriteResponse, err error) {
	mreqs := make([]devices.MemoryWriteRequest, 0)
	rsp = &sni.MultiDomainWriteResponse{
		Responses: make([]*sni.GroupedDomainWriteResponses, len(request.Requests)),
	}
	addressSizes := make([]*sni.MemoryDomainAddressSize, 0)

	for i, domainReqs := range request.Requests {
		snesDomainRef, ok := domainReqs.Domain.Type.(*sni.MemoryDomainRef_Snes)
		if !ok {
			err = status.Errorf(codes.InvalidArgument, "Domain.Type must be of SNES type")
			return
		}

		var dm *domainDesc
		if snesDomainRef.Snes == sni.MemoryDomainTypeSNES_SNESCoreSpecificMemory {
			// look up by string name instead:
			if domainReqs.Domain.Name == nil {
				err = status.Error(codes.InvalidArgument, "domain name must be non-nil when using SNESCoreSpecificMemory type")
				return
			}

			domainName := strings.ToUpper(*domainReqs.Domain.Name)
			dm, ok = domainDescByName[domainName]
			if !ok {
				err = status.Errorf(codes.InvalidArgument, "invalid domain name '%s'", domainName)
				return
			}
		} else {
			dm, ok = domainDescByType[snesDomainRef.Snes]
			if !ok {
				err = status.Errorf(codes.InvalidArgument, "invalid domain type '%s'", snesDomainRef.Snes)
				return
			}
		}

		rsp.Responses[i] = &sni.GroupedDomainWriteResponses{
			Domain: dm.domainRef,
			Writes: make([]*sni.MemoryDomainAddressSize, len(domainReqs.Writes)),
		}
		for j, write := range domainReqs.Writes {
			if write.Address >= dm.size {
				err = status.Errorf(codes.InvalidArgument, "request start address 0x%06x exceeds domain %s size 0x%06x", write.Address, *dm.domainRef.Name, dm.size)
				return
			}
			size := uint32(len(write.Data))
			if write.Address+size > dm.size {
				err = status.Errorf(codes.InvalidArgument, "request end address 0x%06x exceeds domain %s size 0x%06x", write.Address+size, *dm.domainRef.Name, dm.size)
				return
			}

			mreq := devices.MemoryWriteRequest{
				RequestAddress: devices.AddressTuple{
					Address:       dm.start + write.Address,
					AddressSpace:  sni.AddressSpace_FxPakPro,
					MemoryMapping: sni.MemoryMapping_Unknown,
				},
				Data: write.Data,
			}
			mreqs = append(mreqs, mreq)

			addressSize := &sni.MemoryDomainAddressSize{
				Address: write.Address,
				Size:    size,
			}
			addressSizes = append(addressSizes, addressSize)
			rsp.Responses[i].Writes[j] = addressSize
		}
	}

	var mrsp []devices.MemoryWriteResponse
	mrsp, err = d.MultiWriteMemory(ctx, mreqs...)
	if err != nil {
		return
	}

	_ = mrsp

	return
}
