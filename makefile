# Copyright (C) 2014 Zenoss, Inc
#
# zminion is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 2 of the License, or
# (at your option) any later version.
#
# zminion is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
# GNU General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with Foobar. If not, see <http://www.gnu.org/licenses/>.

## setup all environment stuff
URL           = https://github.com/zenoss/zminion
FULL_NAME     = $(shell basename $(URL))
VERSION      := $(shell cat ./VERSION)
DATE         := $(shell date -u '+%a_%b_%d_%H:%M:%S_%Z_%Y')
GIT_COMMIT   ?= $(shell ./hack/gitstatus.sh)
GIT_BRANCH   ?= $(shell git rev-parse --abbrev-ref HEAD)
# jenkins default, jenkins-${JOB_NAME}-${BUILD_NUMBER}
BUILD_TAG    ?= 0

GO_VERSION := $(shell go version | awk '{print $$3}')
GO        = $(shell which go)

MIN_GO_VERSION ?= go1.7

ifeq ($(OS),)
OS := $(shell uname -s)
endif

# Probably not necessary, but you never know
ifeq "$(OS)" "Windows_NT"
$(error Windows is not supported)
endif

LDFLAGS = -ldflags "\
	-X main.Version=$(VERSION) \
	-X main.Gitcommit=$(GIT_COMMIT) \
	-X main.Gitbranch=$(GIT_BRANCH) \
	-X main.GoVersion=$(GO_VERSION) \
	-X main.Date=$(DATE)  \
	-X main.Buildtag=$(BUILD_TAG)"

PKGROOT       = /tmp/$(FULL_NAME)-pkgroot-$(GIT_COMMIT)
DUID         ?= $(shell id -u)
DGID         ?= $(shell id -g)
GOSOURCEFILES := $(shell find `go list -f '{{.Dir}}' ./... | grep -v /vendor/` -maxdepth 1 -name \*.go)
FULL_PATH     = $(shell echo $(URL) | sed 's|https:/||')

.PHONY: build
build: goversion $(FULL_NAME)

## generic workhorse targets
$(FULL_NAME): VERSION *.go hack/* makefile $(GOSOURCEFILES)
	$(GO) build ${LDFLAGS} -o $(FULL_NAME) .
	$(GO) tool vet $(GOSOURCEFILES)
	chown $(DUID):$(DGID) $(FULL_NAME)

# Verify that we are running with the right go version
.PHONY: goversion
goversion:
ifeq "$(shell go version | grep $(MIN_GO_VERSION))" ""
        $(error "Build requires go version $(MIN_GO_VERSION)")
endif

stage_pkg: $(FULL_NAME)
	mkdir -p $(PKGROOT)/usr/bin
	cp -v $(FULL_NAME) $(PKGROOT)/usr/bin

tgz: stage_pkg
	tar cvfz /tmp/$(FULL_NAME)-$(VERSION).tgz -C $(PKGROOT)/usr .
	chown $(DUID):$(DGID) /tmp/$(FULL_NAME)-$(VERSION).tgz
	mv /tmp/$(FULL_NAME)-$(VERSION).tgz .

clean:
	rm -f *.deb
	rm -f *.rpm
	rm -f *.tgz
	rm -fr /tmp/$(FULL_NAME)-pkgroot-*
	rm -f $(FULL_NAME)


