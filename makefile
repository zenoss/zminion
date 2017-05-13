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
	-X main.Date=$(DATE)  \
	-X main.Buildtag=$(BUILD_TAG)"

MAINTAINER    = dev@zenoss.com
# https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/#license-specification
DEB_LICENSE   = "GPL-2.0"
# https://fedoraproject.org/wiki/Licensing:Main?rd=Licensing
RPM_LICENSE   = "GPLv2"
VENDOR        = Zenoss
PKGROOT       = /tmp/$(FULL_NAME)-pkgroot-$(GIT_COMMIT)
DUID         ?= $(shell id -u)
DGID         ?= $(shell id -g)
DESCRIPTION  := A simple shell execution client/server using redis
GOSOURCEFILES := $(shell find `go list -f '{{.Dir}}' ./... | grep -v /vendor/` -maxdepth 1 -name \*.go)
FULL_PATH     = $(shell echo $(URL) | sed 's|https:/||')
DOCKER_WDIR  := /go/src$(FULL_PATH)

.PHONY: build
build: goversion $(FULL_NAME)

## generic workhorse targets
$(FULL_NAME): VERSION *.go hack/* makefile $(GOSOURCEFILES)
	$(GO) build ${LDFLAGS} -o $(FULL_NAME) .
	chown $(DUID):$(DGID) $(FULL_NAME)

docker-build: $(FULL_NAME)-build
	docker run --rm -v `pwd`:$(DOCKER_WDIR) -w $(DOCKER_WDIR) -e DUID=$(DUID) -e DGID=$(DGID) zenoss/$(FULL_NAME)-build:$(VERSION) make

docker-tgz: $(FULL_NAME)-build
	docker run --rm -v `pwd`:$(DOCKER_WDIR) -w $(DOCKER_WDIR) -e DUID=$(DUID) -e DGID=$(DGID) zenoss/$(FULL_NAME)-build:$(VERSION) make tgz

docker-deb: $(FULL_NAME)-build
	docker run --rm -v `pwd`:$(DOCKER_WDIR) -w $(DOCKER_WDIR) -e DUID=$(DUID) -e DGID=$(DGID) zenoss/$(FULL_NAME)-build:$(VERSION) make deb

docker-rpm: $(FULL_NAME)-build
	docker run --rm -v `pwd`:$(DOCKER_WDIR) -w $(DOCKER_WDIR) -e DUID=$(DUID) -e DGID=$(DGID) zenoss/$(FULL_NAME)-build:$(VERSION) make rpm


# Verify that we are running with the right go version
.PHONY: goversion
goversion:
ifeq "$(shell go version | grep $(MIN_GO_VERSION))" ""
        $(error "Build requires go version $(MIN_GO_VERSION)")
endif

# actual work
.PHONY: $(FULL_NAME)-build
$(FULL_NAME)-build:
	docker build -t zenoss/$(FULL_NAME)-build:$(VERSION) hack


stage_pkg: $(FULL_NAME)
	mkdir -p $(PKGROOT)/usr/bin
	cp -v $(FULL_NAME) $(PKGROOT)/usr/bin

tgz: stage_pkg
	tar cvfz /tmp/$(FULL_NAME)-$(VERSION).tgz -C $(PKGROOT)/usr .
	chown $(DUID):$(DGID) /tmp/$(FULL_NAME)-$(VERSION).tgz
	mv /tmp/$(FULL_NAME)-$(VERSION).tgz .

deb: stage_pkg
	fpm \
		-n $(FULL_NAME) \
		-v $(VERSION) \
		-s dir \
		-t deb \
		-a x86_64 \
		-C $(PKGROOT) \
		-m $(MAINTAINER) \
		--description "$(DESCRIPTION)" \
		--deb-user root \
		--deb-group root \
		--license $(DEB_LICENSE) \
		--vendor $(VENDOR) \
		--url $(URL) \
		-f -p /tmp \
		.
	chown $(DUID):$(DGID) /tmp/*.deb
	cp -p /tmp/*.deb .

rpm: stage_pkg
	fpm \
		-n $(FULL_NAME) \
		-v $(VERSION) \
		-s dir \
		-t rpm \
		-a x86_64 \
		-C $(PKGROOT) \
		-m $(MAINTAINER) \
		--description "$(DESCRIPTION)" \
		--rpm-user root \
		--rpm-group root \
		--license $(RPM_LICENSE) \
		--vendor $(VENDOR) \
		--url $(URL) \
		-f -p /tmp \
		.
	chown $(DUID):$(DGID) /tmp/*.rpm
	cp -p /tmp/*.rpm .

clean:
	rm -f *.deb
	rm -f *.rpm
	rm -f *.tgz
	rm -fr /tmp/$(FULL_NAME)-pkgroot-*
	rm -f $(FULL_NAME)


