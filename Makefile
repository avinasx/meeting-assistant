APP := build/bin/MeetingAssistant.app

.PHONY: build run test test-go test-frontend vet clean

# Build the macOS app bundle.
build:
	sh scripts/build.sh

# Build and open the app.
run: build
	open $(APP)

test: test-go test-frontend

test-go:
	go test ./...

# Needs Node.js; no npm packages.
test-frontend:
	node frontend_test/run.js

vet:
	go vet -tags desktop,production ./...

clean:
	rm -rf build/bin
