module sni

go 1.16

require (
	github.com/alttpo/observable v0.0.0-20210711204527-d8b64a4529cc
	github.com/fsnotify/fsnotify v1.4.9
	github.com/getlantern/systray v1.3.0
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.0.4
	github.com/spf13/viper v1.8.1
	go.bug.st/serial v1.3.3
	golang.org/x/net v0.0.0-20210405180319-a5a99cb37ef4
	google.golang.org/grpc v1.38.0
	google.golang.org/protobuf v1.26.0
)

replace github.com/getlantern/systray => github.com/alttpo/systray v1.3.0

//replace github.com/getlantern/systray => ../systray

//replace go.bug.st/serial => ../go-serial
