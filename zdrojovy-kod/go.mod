module nfc-most

go 1.24.7

require (
	fyne.io/systray v1.12.2
	github.com/coder/websocket v1.8.15
	github.com/ebfe/scard v0.0.0-20241214075232-7af069cabc25
	golang.org/x/sys v0.30.0
)

require github.com/godbus/dbus/v5 v5.1.0 // indirect

replace fyne.io/systray => github.com/fyne-io/systray v1.12.2

replace golang.org/x/sys => github.com/golang/sys v0.30.0
