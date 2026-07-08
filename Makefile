.PHONY: all server client client-386 client-amd64 clean

all: server client-386 client-amd64

server:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/perch-server.exe ./cmd/server

client-386:
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o dist/perch-386 ./cmd/client

client-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/perch-amd64 ./cmd/client

client: client-386 client-amd64

clean:
	rm -rf dist
