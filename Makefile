.PHONY: app helper test vet install clean

app:
	scripts/build-app.sh

helper:
	CGO_ENABLED=1 go build -o build/tetherly-helper ./cmd/tetherly-helper

test:
	go test ./...

vet:
	go vet ./...

install: app
	rm -rf /Applications/Tetherly.app
	cp -R build/Tetherly.app /Applications/
	open /Applications/Tetherly.app

clean:
	rm -rf build dist
