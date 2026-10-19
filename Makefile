.PHONY: build test fmt clean

build:
	go build -trimpath -ldflags="-s -w" -o tasksweep ./cmd/tasksweep

test:
	go test ./internal/upstream ./internal/panel

fmt:
	gofmt -w ./cmd ./internal

clean:
	rm -f tasksweep
