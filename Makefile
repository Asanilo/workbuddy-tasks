.PHONY: build test fmt clean release

VERSION ?= dev
DIST := dist

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o tasksweep ./cmd/tasksweep

test:
	go test ./internal/upstream ./internal/panel

fmt:
	gofmt -w ./cmd ./internal

clean:
	rm -f tasksweep
	rm -rf $(DIST)

release:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST)/tasksweep-linux-amd64 ./cmd/tasksweep
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(DIST)/tasksweep-linux-arm64 ./cmd/tasksweep
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST)/tasksweep-darwin-amd64 ./cmd/tasksweep
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(DIST)/tasksweep-darwin-arm64 ./cmd/tasksweep
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(DIST)/tasksweep-windows-amd64.exe ./cmd/tasksweep
	cd $(DIST) && sha256sum tasksweep-* > checksums.txt
	@echo "built $(VERSION) assets in $(DIST)/"
