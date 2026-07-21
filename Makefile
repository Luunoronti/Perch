.PHONY: all server client client-386 client-amd64 client-windows-amd64 winres clean

all: server client-386 client-amd64 client-windows-amd64

# Embeds Windows version metadata (company/product/description) into the
# .exe resources -- helps a little against AV/SmartScreen heuristics on
# unsigned binaries. Uses the versioninfo.json checked in per-cmd; the
# release workflow patches the version numbers from the git tag, but a
# local `make` just uses whatever is committed.
winres:
	cd cmd/client && go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest -o resource_windows_amd64.syso -64 versioninfo.json
	cd cmd/server && go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest -o resource_windows_amd64.syso -64 versioninfo.json

# -H=windowsgui puts the server in the GUI subsystem so tray mode (-tray)
# never flashes a console window (e.g. at login via the Run key). Console
# mode reattaches to the launching terminal at runtime -- see attachConsole.
server: winres
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui" -o dist/perch-server.exe ./cmd/server

client-386:
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o dist/perch-386 ./cmd/client

client-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/perch-amd64 ./cmd/client

client-windows-amd64: winres
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/perch-windows-amd64.exe ./cmd/client

client: client-386 client-amd64 client-windows-amd64

clean:
	rm -rf dist
	rm -f cmd/client/resource_windows_amd64.syso cmd/server/resource_windows_amd64.syso
