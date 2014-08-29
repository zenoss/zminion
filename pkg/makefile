# Copyright 2014, The ZMinion Authors. All rights reserved.
# Use of this source code is governed by the Apache 2.0
# license that can be found in the LICENSE file.

#
# RPM and DEB builder for ZMinion.
#

NAME          ?= zminion
PREFIX        ?= /opt/zenoss
VERSION       ?= $(shell cat ../VERSION)
RELEASE_PHASE ?=
SUBPRODUCT    ?=
MAINTAINER    ="Zenoss CM <cm@zenoss.com>"
PKGROOT       = pkgroot
# https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/#license-specification
DEB_LICENSE   = Apache-2
# https://fedoraproject.org/wiki/Licensing:Main?rd=Licensing
RPM_LICENSE   = "ASL 2.0"
VENDOR        = "Zenoss, Inc."
URL           = https://github.com/zenoss/zminion
DEB_PRIORITY  = extra
CATEGORY      = admin
UID           := $(shell id -u)



ifeq "$(BUILD_NUMBER)" ""
PKG_VERSION = $(VERSION)$(RELEASE_PHASE)
else
PKG_VERSION = $(VERSION)$(RELEASE_PHASE)-$(BUILD_NUMBER)
endif

ifeq "$(SUBPRODUCT)" ""
FULL_NAME=$(NAME)
else
FULL_NAME=$(NAME)-$(SUBPRODUCT)
endif

define DESCRIPTION
Zenoss Zminion runtime 
A simple shell execution client/server using redis
Allows services to be uniformly created, scaled, and managed.
endef
export DESCRIPTION

.PHONY: all clean deb rpm
.SILENT: desc

all: desc

desc:
	echo "Usage: make docker-rpm, make rpm, or make deb. All options package $(FULL_NAME)-$(PKG_VERSION)."

.PHONY: clean_files
clean_files:
	@for pkg in $(FULL_NAME)*.deb $(FULL_NAME)*.rpm ;\
	do \
		if [ -f "$${pkg}" ];then \
			echo "rm -f $${pkg}" ;\
			if ! rm -f $${pkg} ;then \
				echo "sudo rm -f $${pkg}" ;\
				if ! sudo rm -f $${pkg} ; then \
					echo "Warning: Unable to remove $${pkg}" ;\
					exit 1 ;\
				fi ;\
			fi ;\
		fi ;\
	done

.PHONY: clean_dirs
clean_dirs = $(PKGROOT) build
clean_dirs: 
	@for dir in $(clean_dirs) ;\
	do \
		if [ -d "$${dir}" ];then \
			echo "rm -rf $${dir}" ;\
			if ! rm -rf $${dir} ;then \
				echo "sudo rm -rf $${dir}" ;\
				if ! sudo rm -rf $${dir} ; then \
					echo "Warning: Unable to remove $${dir}" ;\
					exit 1 ;\
				fi ;\
			fi ;\
		fi ;\
	done

# Clean staged files and produced packages
.PHONY: clean
clean: clean_files clean_dirs

.PHONY: mrclean
mrclean: clean

# Make root dir for packaging
$(PKGROOT):
	mkdir -p $@

# Build zminion binary
build:
	cd .. && $(MAKE)

fpm_dependencies:
	yum install -y gem ruby-devel
	gem install fpm
	echo "installed dependencies"

stage_deb: build
	cd .. && $(MAKE) clean
	make clean_files clean_dirs clean_dirs=$(PKGROOT) $(PKGROOT)
	cd .. && $(MAKE) install PREFIX=$(abspath $(PKGROOT))/$(PREFIX)

stage_rpm: build
	cd .. && $(MAKE) clean
	make clean_files clean_dirs clean_dirs=$(PKGROOT) $(PKGROOT)
	cd .. && $(MAKE) install PREFIX=$(abspath $(PKGROOT))/$(PREFIX)

# Make a DEB
# net-tools provides ifconfig, needed for VIPs
deb: stage_deb fpm_dependencies
	echo "creating $(@) takes a few minutes"
	fpm \
		-n $(FULL_NAME) \
		-v $(PKG_VERSION)~$$(lsb_release -cs) \
		-s dir \
		-t deb \
		-a x86_64 \
		-C $(PKGROOT) \
		-m $(MAINTAINER) \
		--description "$$DESCRIPTION" \
		--deb-user root \
		--deb-group root \
		--deb-priority $(DEB_PRIORITY) \
		--license $(DEB_LICENSE) \
		--vendor $(VENDOR) \
		--url $(URL) \
		--category $(CATEGORY) \
		.

# Make an RPM
rpm: stage_rpm fpm_dependencies
	echo "creating $(@) takes a few minutes"
	fpm \
		-n $(FULL_NAME) \
		-v $(PKG_VERSION) \
		-s dir \
		-t rpm \
		-a x86_64 \
		-C $(PKGROOT) \
		-m $(MAINTAINER) \
		--description "$$DESCRIPTION" \
		--rpm-user root \
		--rpm-group root \
		--license $(RPM_LICENSE) \
		--vendor $(VENDOR) \
		--url $(URL) \
		--category $(CATEGORY) \
		.
#		-d "supervisor" \

docker-rpm:
	docker run --rm -w /mnt/$(NAME)/pkg -v $(shell cd .. && pwd):/mnt/$(NAME) zenoss/rpmbuild:centos7 \
	   	bash -c "make rpm && find /mnt/$(NAME) -exec chown $(UID):$(UID) {} \\;"

