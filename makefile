PREFIX ?= $(ZENHOME)
PACKAGE=github.com/zenoss/zminion


.PHONY: default
default: build

.PHONY: build
build: zminion

zminion:
	@go get $(PACKAGE)
	@go build $(PACKAGE)

.PHONY: install
install: zminion
	@mkdir -p $(PREFIX)/bin
	@install -m 755 zminion $(PREFIX)/bin/zminion

.PHONY: test
test:
	@go get $(PACKAGE)
	@go test $(PACKAGE)

scratchbuild:
	@export GOPATH=/tmp/zminion-build; \
		BUILDDIR=$$GOPATH/src/$(PACKAGE); \
		HERE=$$PWD; \
		mkdir -p $$BUILDDIR; \
		rsync -rad $$HERE/ $$BUILDDIR ; \
		cd $$BUILDDIR; \
		$(MAKE) clean build; \
		mv $$BUILDDIR/zminion $$HERE/

.PHONY: clean
clean:
	@go clean
