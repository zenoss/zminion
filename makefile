# docker run -i -t -w /mnt/src -v $(pwd):/mnt/src zenoss/rpmbuild:centos7 bash
# 	yum install gem
# 	gem install fpm

PREFIX ?= /opt/serviced
PACKAGE=github.com/zenoss/zminion

VERSION := $(shell cat ./VERSION)
DATE := '$(shell date -u)'

# GIT_URL ?= $(shell git remote -v | awk '$3 == "(fetch)" {print $$2}')
# assume it will get set because the above can cause network traffic on every run
GIT_COMMIT ?= $(shell ./gitstatus.sh)
GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)

# jenkins default, jenkins-${JOB_NAME}-${BUILD_NUMBER}
BUILD_TAG ?= 0


LDFLAGS = -ldflags "-X main.Version $(VERSION) -X main.Giturl '$(GIT_URL)' -X main.Gitcommit $(GIT_COMMIT) -X main.Gitbranch $(GIT_BRANCH) -X main.Date $(DATE) -X main.Buildtag $(BUILD_TAG)"

.PHONY: default
default: build

#------------------------------------------------------------------------------#
# Define GOPATH for containerized builds.
#
#    NB: Keep this in sync with build/Dockerfile: ENV GOPATH /go
#
docker_GOPATH = /go

ifeq "$(GOPATH)" ""
    $(warning "GOPATH not set. Ok to ignore for containerized builds.")
else
    GOSRC = $(GOPATH)/src
    GOBIN = $(GOPATH)/bin
    GOPKG = $(GOPATH)/pkg
endif

#------------------------------------------------------------------------------#
# copied from https://github.com/control-center/serviced/blob/develop/makefile
#------------------------------------------------------------------------------#
GODEP     = $(GOBIN)/godep
Godeps    = Godeps
godep_SRC = github.com/tools/godep

Godeps_restored = .Godeps_restored
$(Godeps_restored): $(GODEP) $(Godeps)
	@echo "$(GODEP) restore" ;\
	$(GODEP) restore ;\
	rc=$$? ;\
	if [ $${rc} -ne 0 ] ; then \
		echo "ERROR: Failed $(GODEP) restore. [rc=$${rc}]" ;\
		echo "** Unable to restore your GOPATH to a baseline state." ;\
		echo "** Perhaps internet connectivity is down." ;\
		exit $${rc} ;\
	fi
	touch $@


# Download godep source to $GOPATH/src/.
$(GOSRC)/$(godep_SRC):
	go get $(godep_SRC)

missing_godep_SRC = $(filter-out $(wildcard $(GOSRC)/$(godep_SRC)), $(GOSRC)/$(godep_SRC))
$(GODEP): | $(missing_godep_SRC)
	go install $(godep_SRC)

.PHONY: clean_godeps
clean_godeps: | $(GODEP) $(Godeps)
	-$(GODEP) restore && go clean -r && go clean -i github.com/zenoss/zminion/... # this cleans all dependencies
	@if [ -f "$(Godeps_restored)" ];then \
		rm -f $(Godeps_restored) ;\
		echo "rm -f $(Godeps_restored)" ;\
	fi


.PHONY: build
build: zminion

zminion: $(Godeps_restored)
	$(GODEP) restore $(PACKAGE)
	go build -x
	#go install

zminion = $(GOBIN)/zminion
$(zminion): $(Godeps_restored)
$(zminion): FORCE
	go install ${LDFLAGS}


.PHONY: install
install: zminion
	mkdir -p $(PREFIX)/bin
	install -m 755 zminion $(PREFIX)/bin/zminion

.PHONY: test
test:
	@go get $(PACKAGE)
	@go test $(PACKAGE)

.PHONY: clean
clean: clean_godeps
	@go clean

