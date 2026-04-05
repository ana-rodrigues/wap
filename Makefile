.PHONY: build install clean

VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -s -w"

build:
	go build $(LDFLAGS) -o wap-cli .

install: build
	install -m 755 wap-cli /usr/local/bin/wap-cli

clean:
	rm -f wap-cli
