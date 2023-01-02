package fxpakpro

import (
	"fmt"
	"github.com/mitchellh/mapstructure"
	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
	"log"
	"net/url"
	"runtime"
	"sni/devices"
	"sni/devices/platforms"
	"sni/protos/sni"
	"sni/util"
	"sni/util/env"
	"strconv"
	"strings"
)

const (
	driverName = "fxpakpro"
)

var driver *Driver

var (
	baudRates = []int{
		921600, // first rate that works on Windows
		460800,
		256000,
		230400, // first rate that works on MacOS
		153600,
		128000,
		115200,
		76800,
		57600,
		38400,
		28800,
		19200,
		14400,
		9600,
	}
)

const defaultAddressSpace = sni.AddressSpace_FxPakPro

type Driver struct {
	container devices.DeviceContainer
}

func (d *Driver) DisplayOrder() int {
	return 0
}

func (d *Driver) DisplayName() string {
	return "FX Pak Pro"
}

func (d *Driver) DisplayDescription() string {
	return "Connect to an FX Pak Pro or SD2SNES via USB"
}

func (d *Driver) Kind() string { return "fxpakpro" }

var driverCapabilities = []sni.DeviceCapability{
	sni.DeviceCapability_ReadMemory,
	sni.DeviceCapability_WriteMemory,
	sni.DeviceCapability_ResetSystem,
	sni.DeviceCapability_ResetToMenu,
	sni.DeviceCapability_ExecuteASM,
	sni.DeviceCapability_FetchFields,
	// filesystem:
	sni.DeviceCapability_ReadDirectory,
	sni.DeviceCapability_MakeDirectory,
	sni.DeviceCapability_RemoveFile,
	sni.DeviceCapability_RenameFile,
	sni.DeviceCapability_PutFile,
	sni.DeviceCapability_GetFile,
	sni.DeviceCapability_BootFile,
	// memory domains:
	sni.DeviceCapability_ReadMemoryDomain,
	sni.DeviceCapability_WriteMemoryDomain,
}

func (d *Driver) HasCapabilities(capabilities ...sni.DeviceCapability) (bool, error) {
	return devices.CheckCapabilities(capabilities, driverCapabilities)
}

func (d *Driver) DisconnectAll() {
	for _, deviceKey := range d.container.AllDeviceKeys() {
		device, ok := d.container.GetDevice(deviceKey)
		if ok {
			log.Printf("%s: disconnecting device '%s'\n", driverName, deviceKey)
			_ = device.Close()
			d.container.DeleteDevice(deviceKey)
		}
	}
}

func (d *Driver) Detect() (devs []devices.DeviceDescriptor, err error) {
	var ports []*enumerator.PortDetails

	devs = make([]devices.DeviceDescriptor, 0, 2)

	ports, err = enumerator.GetDetailedPortsList()
	if err != nil {
		return
	}

	for _, port := range ports {
		if !port.IsUSB {
			continue
		}

		// When more than one fxpakpro is connected only one of the devices gets the SerialNumber="DEMO00000000";
		// This is likely a bug in serial library.
		if (port.SerialNumber == "DEMO00000000") || (port.VID == "1209" && port.PID == "5A22") {
			devs = append(devs, devices.DeviceDescriptor{
				Uri:                 url.URL{Scheme: driverName, Host: ".", Path: port.Name},
				DisplayName:         fmt.Sprintf("%s (%s:%s)", port.Name, port.VID, port.PID),
				Kind:                d.Kind(),
				Capabilities:        driverCapabilities[:],
				DefaultAddressSpace: defaultAddressSpace,
				System:              "snes",
			})
		}
	}

	err = nil
	return
}

func (d *Driver) openPort(portName string, baudRequest int) (f serial.Port, err error) {
	f = serial.Port(nil)

	// Try all the common baud rates in descending order:
	var baud int
	for _, baud = range baudRates {
		if baud > baudRequest {
			continue
		}

		log.Printf("%s: open(name=\"%s\", baud=%d)\n", driverName, portName, baud)
		f, err = serial.Open(portName, &serial.Mode{
			BaudRate: baud,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		})
		if err == nil {
			break
		}
		log.Printf("%s: open(name=\"%s\"): %v\n", driverName, portName, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: failed to open serial port at any baud rate: %w", driverName, err)
	}

	// set DTR:
	//log.Printf("serial: Set DTR on\n")
	if err = f.SetDTR(true); err != nil {
		//log.Printf("serial: %v\n", err)
		_ = f.Close()
		return nil, fmt.Errorf("%s: failed to set DTR: %w", driverName, err)
	}

	return
}

func (d *Driver) DeviceKey(uri *url.URL) (key string) {
	key = uri.Path
	// macos/linux paths:
	if strings.HasPrefix(key, "/dev/") {
		key = key[len("/dev/"):]
	}
	if strings.HasPrefix(key, "cu.usbmodem") {
		key = key[len("cu.usbmodem"):]
	}
	// macos   key should look like `DEMO000000001` with the final `1` suffix being the device index if multiple are connected.
	// windows key should look like `COM4` with the port number varying
	// linux   no idea what these devices look like yet, likely `/dev/...` possibly `ttyUSB0`?
	return
}

func (d *Driver) openDevice(uri *url.URL) (device devices.Device, err error) {
	portName := uri.Path

	var baudRequest int
	if runtime.GOOS == "darwin" {
		baudRequest = baudRates[3]
	} else {
		baudRequest = baudRates[0]
	}
	if baudStr := uri.Query().Get("baud"); baudStr != "" {
		baudRequest, _ = strconv.Atoi(baudStr)
	}

	var f serial.Port
	f, err = d.openPort(portName, baudRequest)
	if err != nil {
		return
	}

	dev := &Device{c: &fxpakCommands{f: f}}
	err = dev.Init()

	device = dev
	return
}

func (d *Driver) Device(uri *url.URL) devices.AutoCloseableDevice {
	return devices.NewAutoCloseableDevice(
		d.container,
		uri,
		d.DeviceKey(uri),
	)
}

var debugLog *log.Logger

func DriverConfig(config *platforms.Config) {
	var ok bool
	var err error

	var confIntf interface{}
	confIntf, ok = config.Drivers["fxpakpro"]
	if !ok {
		log.Printf("fxpakpro: config: missing fxpakpro driver config\n")
		return
	}

	conf := confIntf.(map[string]interface{})

	{
		// translate general domain configurations into our driver-specific domains:
		snesPlatform, ok := config.ByName["snes"]
		if !ok {
			log.Printf("fxpakpro: config: no snes platform defined\n")
			return
		}

		domainByName = make(map[string]*snesDomain)

		allSnesDomains := snesPlatform.Domains
		allDomains = make([]snesDomain, len(allSnesDomains))
		for i, domainConf := range allSnesDomains {
			allDomains[i] = snesDomain{
				Domain: platforms.Domain{
					DomainConf:     *domainConf,
					IsExposed:      false,
					IsCoreSpecific: false,
					// readable/writable status is driver-specific:
					IsReadable:  false,
					IsWriteable: false,
				},
				start: 0,
			}
			domainByName[strings.ToLower(domainConf.Name)] = &allDomains[i]
		}
	}

	// read fxpakpro specific domain details:
	{
		var config struct {
			Domains []*struct {
				Name      string
				Space     string
				Start     uint32
				Size      *uint64
				Readable  bool
				Writeable bool
			}
		}

		err = mapstructure.Decode(conf, &config)
		if err != nil {
			log.Printf("fxpakpro: config: %s\n", err)
			return
		}

		for _, domainConf := range config.Domains {
			nameLower := strings.ToLower(domainConf.Name)

			var d *snesDomain
			d, ok = domainByName[nameLower]
			if !ok {
				// create a new domain:
				allDomains = append(allDomains, snesDomain{
					Domain: platforms.Domain{
						DomainConf: platforms.DomainConf{
							Name: domainConf.Name,
						},
						IsCoreSpecific: true,
					},
					start: 0,
				})
				d = &allDomains[len(allDomains)-1]
				domainByName[nameLower] = d
			}

			// override properties:
			if domainConf.Size != nil {
				d.Size = *domainConf.Size
			}
			d.IsReadable = domainConf.Readable
			d.IsWriteable = domainConf.Writeable
			d.space = domainConf.Space
			d.start = domainConf.Start

			// mark domain as exposed:
			d.IsExposed = true
		}
	}
}

func DriverInit() {
	if util.IsTruthy(env.GetOrDefault("SNI_FXPAKPRO_DISABLE", "0")) {
		log.Printf("disabling fxpakpro snes driver\n")
		return
	}

	if util.IsTruthy(env.GetOrDefault("SNI_DEBUG", "0")) {
		defaultLogger := log.Default()
		debugLog = log.New(
			defaultLogger.Writer(),
			fmt.Sprintf("fxpakpro: "),
			defaultLogger.Flags()|log.Lmsgprefix,
		)
	}

	driver = &Driver{}
	driver.container = devices.NewDeviceDriverContainer(driver.openDevice)
	devices.Register(driverName, driver)
}
